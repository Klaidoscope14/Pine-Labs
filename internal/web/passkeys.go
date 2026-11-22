package web

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"aegis/internal/config"
)

// Passkeys (PLAN.md §10.8): users register a WebAuthn credential; approving a
// run needs a fresh assertion whose challenge is bound to that exact change, so
// a stolen session or a forged API call can't approve.

type waUser struct {
	u     config.APIUser
	creds []webauthn.Credential
}

func (w waUser) WebAuthnID() []byte {
	h := sha256.Sum256([]byte(w.u.Tenant + "/" + w.u.ID))
	return h[:]
}
func (w waUser) WebAuthnName() string                       { return w.u.ID }
func (w waUser) WebAuthnDisplayName() string                { return w.u.Name }
func (w waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

type pending struct {
	data    webauthn.SessionData
	expires time.Time
	// For approvals: what the challenge was bound to.
	run, base, head, evidence string
}

type passkeys struct {
	wa *webauthn.WebAuthn
	mu sync.Mutex
	m  map[string]pending
}

func newPasskeys(cfg *config.Config) (*passkeys, error) {
	rp := cfg.API.WebAuthnRPID
	if rp == "" {
		rp = "localhost"
	}
	origins := cfg.API.WebAuthnOrigins
	// The frontend's public origin (e.g. https://aegis.vercel.app) is set at deploy
	// time; passkeys are then bound to that site.
	if o := os.Getenv("AEGIS_WEBAUTHN_ORIGIN"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			return nil, fmt.Errorf("AEGIS_WEBAUTHN_ORIGIN must be an https origin, got %q", o)
		}
		rp, origins = u.Hostname(), []string{u.Scheme + "://" + u.Host}
	}
	if len(origins) == 0 {
		_, port, _ := net.SplitHostPort(cfg.API.Listen)
		origins = []string{"http://localhost:" + port}
	}
	wa, err := webauthn.New(&webauthn.Config{RPDisplayName: "Aegis", RPID: rp, RPOrigins: origins})
	if err != nil {
		return nil, err
	}
	return &passkeys{wa: wa, m: map[string]pending{}}, nil
}

func (p *passkeys) put(key string, v pending) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, x := range p.m { // sweep expired ceremonies
		if now.After(x.expires) {
			delete(p.m, k)
		}
	}
	v.expires = now.Add(3 * time.Minute)
	p.m[key] = v
}

// take returns and removes a pending ceremony (single use).
func (p *passkeys) take(key string) (pending, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.m[key]
	delete(p.m, key)
	if ok && time.Now().After(v.expires) {
		return pending{}, false
	}
	return v, ok
}

func (s *Server) loadWAUser(r *http.Request) (waUser, error) {
	u := userFrom(r)
	stored, err := s.Store.Passkeys(r.Context(), u.Tenant, u.ID)
	if err != nil {
		return waUser{}, err
	}
	w := waUser{u: u}
	for _, js := range stored {
		var c webauthn.Credential
		if json.Unmarshal([]byte(js), &c) == nil {
			w.creds = append(w.creds, c)
		}
	}
	return w, nil
}

func credID(c *webauthn.Credential) string { return base64.RawURLEncoding.EncodeToString(c.ID) }

// ---- registration ----

func (s *Server) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	u, err := s.loadWAUser(r)
	if err != nil {
		apiErr(w, 500, err)
		return
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range u.creds {
		exclude = append(exclude, c.Descriptor())
	}
	opts, sess, err := s.pk.wa.BeginRegistration(u, webauthn.WithExclusions(exclude),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred))
	if err != nil {
		apiErr(w, 500, err)
		return
	}
	s.pk.put("reg:"+u.u.Tenant+"/"+u.u.ID, pending{data: *sess})
	writeJSON(w, 200, opts)
}

func (s *Server) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	u, err := s.loadWAUser(r)
	if err != nil {
		apiErr(w, 500, err)
		return
	}
	p, ok := s.pk.take("reg:" + u.u.Tenant + "/" + u.u.ID)
	if !ok {
		apiErr(w, 400, errors.New("registration expired; try again"))
		return
	}
	cred, err := s.pk.wa.FinishRegistration(u, p.data, limitBody(w, r))
	if err != nil {
		apiErr(w, 400, fmt.Errorf("passkey not accepted: %v", err))
		return
	}
	js, _ := json.Marshal(cred)
	if err := s.Store.AddPasskey(r.Context(), u.u.Tenant, u.u.ID, credID(cred), string(js)); err != nil {
		apiErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "registered"})
}

// ---- signed approval ----

// approvalBinding is what the approval challenge commits to.
func (s *Server) approvalBinding(r *http.Request, id string) (base, head, evidence string, err error) {
	sum, err := s.Store.Summary(r.Context(), userFrom(r).Tenant, id)
	if err != nil {
		return "", "", "", errors.New("no such run")
	}
	var pkt struct {
		Evidence string `json:"evidence_hash"`
	}
	data, err := os.ReadFile(filepath.Join(s.runDir(id), "release_packet.json"))
	if err != nil || json.Unmarshal(data, &pkt) != nil || pkt.Evidence == "" {
		return "", "", "", errors.New("the evidence packet isn't ready")
	}
	return sum.Run.BaseSHA, sum.Run.HeadSHA, pkt.Evidence, nil
}

func (s *Server) approveBegin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := s.loadWAUser(r)
	if err != nil {
		apiErr(w, 500, err)
		return
	}
	if len(u.creds) == 0 {
		apiErr(w, 400, errors.New("register a passkey in Settings first"))
		return
	}
	meta, err := s.Store.RunMeta(r.Context(), u.u.Tenant, id)
	if err != nil || meta.Status != "AWAITING_HUMAN" {
		apiErr(w, 400, errors.New("this run is not waiting for a decision"))
		return
	}
	base, head, evidence, err := s.approvalBinding(r, id)
	if err != nil {
		apiErr(w, 400, err)
		return
	}
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	h := sha256.New()
	fmt.Fprintf(h, "aegis-approve\n%s\n%s\n%s\n%s\n", id, base, head, evidence)
	h.Write(nonce)
	opts, sess, err := s.pk.wa.BeginLogin(u, webauthn.WithChallenge(h.Sum(nil)),
		webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		apiErr(w, 500, err)
		return
	}
	s.pk.put("appr:"+u.u.Tenant+"/"+u.u.ID+"/"+id, pending{data: *sess, run: id, base: base, head: head, evidence: evidence})
	writeJSON(w, 200, opts)
}

func (s *Server) approveFinish(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := s.loadWAUser(r)
	if err != nil {
		apiErr(w, 500, err)
		return
	}
	p, ok := s.pk.take("appr:" + u.u.Tenant + "/" + u.u.ID + "/" + id)
	if !ok {
		apiErr(w, 400, errors.New("approval expired; try again"))
		return
	}
	// The change must not have moved between challenge and signature.
	if base, head, evidence, err := s.approvalBinding(r, id); err != nil || base != p.base || head != p.head || evidence != p.evidence {
		apiErr(w, 409, errors.New("the change was updated; review it again"))
		return
	}
	cred, err := s.pk.wa.FinishLogin(u, p.data, limitBody(w, r))
	if err != nil {
		apiErr(w, 400, fmt.Errorf("passkey check failed: %v", err))
		return
	}
	js, _ := json.Marshal(cred)
	_ = s.Store.UpdatePasskey(r.Context(), u.u.Tenant, u.u.ID, credID(cred), string(js))
	_ = s.Store.RecordEvent(r.Context(), u.u.Tenant, id, "human.passkey_assertion", map[string]any{
		"reviewer": u.u.ID, "credential": credID(cred), "challenge": p.data.Challenge,
		"base_sha": p.base, "head_sha": p.head, "evidence_hash": p.evidence, "sign_count": cred.Authenticator.SignCount})
	if err := s.decide(r.Context(), who(r), id, true, "approved with passkey", true, credID(cred)); err != nil {
		apiErr(w, 400, err)
		return
	}
	writeJSON(w, 202, map[string]string{"status": "approved"})
}

func limitBody(w http.ResponseWriter, r *http.Request) *http.Request {
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	return r2
}
