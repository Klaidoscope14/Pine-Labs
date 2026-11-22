package web

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/webauthn"

	"aegis/internal/config"
)

// softKey is a minimal software authenticator (ES256, "none" attestation), so the
// server-side WebAuthn verification is tested end to end without hardware.
type softKey struct {
	priv   *ecdsa.PrivateKey
	id     []byte
	origin string
	rpID   string
	count  uint32
}

func newSoftKey(t *testing.T) *softKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softKey{priv: k, id: id, origin: "http://localhost:8480", rpID: "localhost"}
}

var b64 = base64.RawURLEncoding

func (k *softKey) authData(flags byte, attested bool) []byte {
	rp := sha256.Sum256([]byte(k.rpID))
	var b bytes.Buffer
	b.Write(rp[:])
	b.WriteByte(flags)
	_ = binary.Write(&b, binary.BigEndian, k.count)
	if attested {
		b.Write(make([]byte, 16)) // AAGUID
		_ = binary.Write(&b, binary.BigEndian, uint16(len(k.id)))
		b.Write(k.id)
		x, y := k.priv.PublicKey.X.FillBytes(make([]byte, 32)), k.priv.PublicKey.Y.FillBytes(make([]byte, 32))
		cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
		b.Write(cose)
	}
	return b.Bytes()
}

func clientData(typ, challenge, origin string) []byte {
	js, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return js
}

func (k *softKey) register(challenge string) []byte {
	ad := k.authData(0x45, true) // UP | UV | AT
	att, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": ad})
	body, _ := json.Marshal(map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(clientData("webauthn.create", challenge, k.origin)),
			"attestationObject": b64.EncodeToString(att)}})
	return body
}

func (k *softKey) assert(challenge string, userHandle []byte) []byte {
	k.count++
	ad := k.authData(0x05, false) // UP | UV
	cd := clientData("webauthn.get", challenge, k.origin)
	h := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	body, _ := json.Marshal(map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{"authenticatorData": b64.EncodeToString(ad), "clientDataJSON": b64.EncodeToString(cd),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(userHandle)}})
	return body
}

func TestPasskeyRegisterAndChangeBoundApproval(t *testing.T) {
	cfg := &config.Config{API: config.API{Listen: "127.0.0.1:8480", WebAuthnRPID: "localhost", WebAuthnOrigins: []string{"http://localhost:8480"}}}
	pk, err := newPasskeys(cfg)
	if err != nil {
		t.Fatal(err)
	}
	user := waUser{u: config.APIUser{ID: "arjun", Name: "Arjun Mehta", Tenant: "t1"}}
	key := newSoftKey(t)

	// Registration.
	_, sess, err := pk.wa.BeginRegistration(user)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := pk.wa.FinishRegistration(user, *sess, httptest.NewRequest("POST", "/", bytes.NewReader(key.register(sess.Challenge))))
	if err != nil {
		t.Fatalf("registration rejected: %v", err)
	}
	user.creds = []webauthn.Credential{*cred}

	// Approval: the challenge is bound to the change.
	bound := sha256.Sum256([]byte("aegis-approve\nrun-1\nbase\nhead\nevidence\n"))
	_, sess, err = pk.wa.BeginLogin(user, webauthn.WithChallenge(bound[:]))
	if err != nil {
		t.Fatal(err)
	}
	if sess.Challenge != b64.EncodeToString(bound[:]) {
		t.Fatalf("challenge not bound to the change: %s", sess.Challenge)
	}
	if _, err := pk.wa.FinishLogin(user, *sess, httptest.NewRequest("POST", "/", bytes.NewReader(key.assert(sess.Challenge, user.WebAuthnID())))); err != nil {
		t.Fatalf("valid signed approval rejected: %v", err)
	}

	// A signature over a different change is refused.
	other := sha256.Sum256([]byte("aegis-approve\nrun-1\nbase\nOTHER-HEAD\nevidence\n"))
	_, sess, _ = pk.wa.BeginLogin(user, webauthn.WithChallenge(bound[:]))
	if _, err := pk.wa.FinishLogin(user, *sess, httptest.NewRequest("POST", "/", bytes.NewReader(key.assert(b64.EncodeToString(other[:]), user.WebAuthnID())))); err == nil {
		t.Fatal("assertion for another change accepted")
	}

	// A different authenticator (not registered) is refused.
	stranger := newSoftKey(t)
	_, sess, _ = pk.wa.BeginLogin(user, webauthn.WithChallenge(bound[:]))
	if _, err := pk.wa.FinishLogin(user, *sess, httptest.NewRequest("POST", "/", bytes.NewReader(stranger.assert(sess.Challenge, user.WebAuthnID())))); err == nil {
		t.Fatal("unregistered authenticator accepted")
	}

	// A wrong origin (phishing site) is refused.
	key.origin = "http://evil.example"
	_, sess, _ = pk.wa.BeginLogin(user, webauthn.WithChallenge(bound[:]))
	if _, err := pk.wa.FinishLogin(user, *sess, httptest.NewRequest("POST", "/", bytes.NewReader(key.assert(sess.Challenge, user.WebAuthnID())))); err == nil {
		t.Fatal("assertion from another origin accepted")
	}
}

func TestPendingCeremoniesAreSingleUse(t *testing.T) {
	pk, _ := newPasskeys(&config.Config{API: config.API{Listen: "127.0.0.1:8480"}})
	pk.put("k", pending{run: "r"})
	if _, ok := pk.take("k"); !ok {
		t.Fatal("ceremony missing")
	}
	if _, ok := pk.take("k"); ok {
		t.Fatal("ceremony reusable (replay)")
	}
}
