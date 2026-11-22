// Package web is the AegisSDLC web app and JSON API (PLAN.md §10.7).
//
// Server-rendered pages (html/template) plus one small vanilla-JS file: no npm
// supply chain, one binary, a strict CSP with no inline script. Identity is a
// demo user picked at sign-in, kept in an HMAC-signed cookie; every read and
// write is scoped to the user's tenant by the ledger's row-level security.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/client"

	"aegis/internal/config"
	"aegis/internal/ledger"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server serves the web app and API.
type Server struct {
	Cfg      *config.Config
	Store    *ledger.Store
	Temporal client.Client
	AdminDSN string
	Log      *slog.Logger

	key  []byte
	tmpl map[string]*template.Template
	pk   *passkeys
	// BFF is the shared secret the Next.js server (Vercel) sends as a bearer
	// token; with it, X-Aegis-User names the signed-in user. Empty = disabled.
	BFF []byte
	// OriginSecret, when set, must arrive as X-Origin-Verify on every request
	// that doesn't come from loopback: only CloudFront knows it.
	OriginSecret []byte
}

// New builds a server. sessionKey must be at least 32 bytes.
func New(cfg *config.Config, store *ledger.Store, tc client.Client, adminDSN string, sessionKey []byte, log *slog.Logger) (*Server, error) {
	if len(sessionKey) < 32 {
		return nil, errors.New("session key must be at least 32 bytes")
	}
	s := &Server{Cfg: cfg, Store: store, Temporal: tc, AdminDSN: adminDSN, Log: log, key: sessionKey}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	pk, err := newPasskeys(cfg)
	if err != nil {
		return nil, err
	}
	s.pk = pk
	return s, nil
}

func (s *Server) parseTemplates() error {
	s.tmpl = map[string]*template.Template{}
	for _, page := range []string{"login", "runs", "run", "new", "settings", "error"} {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/"+page+".html")
		if err != nil {
			return err
		}
		s.tmpl[page] = t
	}
	return nil
}

// Handler returns the HTTP handler with every route and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })

	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.auth(s.logout))

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/runs", http.StatusSeeOther) })
	mux.HandleFunc("GET /runs", s.auth(s.runsPage))
	mux.HandleFunc("GET /runs/{id}", s.auth(s.runPage))
	mux.HandleFunc("GET /new", s.auth(s.newPage))
	mux.HandleFunc("POST /runs", s.auth(s.createRunForm))
	mux.HandleFunc("POST /runs/{id}/decision", s.auth(s.decisionForm))
	mux.HandleFunc("GET /settings", s.auth(s.settingsPage))
	mux.HandleFunc("POST /settings/kill", s.auth(s.killForm))
	mux.HandleFunc("POST /settings/unkill", s.auth(s.unkillForm))
	mux.HandleFunc("GET /events", s.auth(s.events))

	mux.HandleFunc("GET /api/runs", s.auth(s.apiListRuns))
	mux.HandleFunc("POST /api/runs", s.auth(s.apiCreateRun))
	mux.HandleFunc("GET /api/runs/{id}", s.auth(s.apiGetRun))
	mux.HandleFunc("POST /api/runs/{id}/decision", s.auth(s.apiDecision))
	mux.HandleFunc("POST /api/kill", s.auth(s.apiKill))
	mux.HandleFunc("POST /api/unkill", s.auth(s.apiUnkill))
	mux.HandleFunc("GET /api/me", s.auth(s.apiMe))
	mux.HandleFunc("GET /api/users", s.service(s.apiUsers))
	mux.HandleFunc("GET /api/views/runs", s.auth(s.apiRunsView))
	mux.HandleFunc("GET /api/views/runs/{id}", s.auth(s.apiRunView))
	mux.HandleFunc("POST /api/passkeys/register/begin", s.auth(s.passkeyRegisterBegin))
	mux.HandleFunc("POST /api/passkeys/register/finish", s.auth(s.passkeyRegisterFinish))
	mux.HandleFunc("POST /api/runs/{id}/approve/begin", s.auth(s.approveBegin))
	mux.HandleFunc("POST /api/runs/{id}/approve/finish", s.auth(s.approveFinish))
	return s.secure(mux)
}

// fromLoopback reports a request from this host (the SSM tunnel, local tools).
func fromLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// secure adds security headers and recovers from panics.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Off-host requests must come through CloudFront (which adds the secret header).
		if len(s.OriginSecret) > 0 && !fromLoopback(r) && r.URL.Path != "/healthz" &&
			!hmac.Equal([]byte(r.Header.Get("X-Origin-Verify")), s.OriginSecret) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		defer func() {
			if v := recover(); v != nil {
				s.Log.Error("panic", "path", r.URL.Path, "error", v)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---- identity ----

type ctxKey struct{}

func userFrom(r *http.Request) config.APIUser {
	u, _ := r.Context().Value(ctxKey{}).(config.APIUser)
	return u
}

func (u userView) has(role string) bool { return slices.Contains(u.Roles, role) }

const cookieName = "aegis_session"
const sessionTTL = 12 * time.Hour

func (s *Server) sign(payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) user(id string) (config.APIUser, bool) {
	for _, u := range s.Cfg.API.Users {
		if u.ID == id {
			return u, true
		}
	}
	return config.APIUser{}, false
}

func (s *Server) setSession(w http.ResponseWriter, id string) {
	exp := strconv.FormatInt(time.Now().Add(sessionTTL).Unix(), 10)
	payload := id + "|" + exp
	v := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + s.sign(payload)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: v, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: s.Cfg.Deployment.Environment == "production", MaxAge: int(sessionTTL.Seconds())})
}

func (s *Server) session(r *http.Request) (config.APIUser, string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return config.APIUser{}, "", false
	}
	b64, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return config.APIUser{}, "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil || !hmac.Equal([]byte(sig), []byte(s.sign(string(raw)))) {
		return config.APIUser{}, "", false
	}
	id, exp, _ := strings.Cut(string(raw), "|")
	if n, err := strconv.ParseInt(exp, 10, 64); err != nil || time.Now().Unix() > n {
		return config.APIUser{}, "", false
	}
	u, ok := s.user(id)
	return u, string(raw), ok
}

// csrf is bound to the session, so a token can't be replayed across sessions.
func (s *Server) csrf(session string) string { return s.sign("csrf|" + session)[:32] }

// auth requires a session; for unsafe methods it also requires the CSRF token
// (form field "csrf" or header X-CSRF-Token).
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Server-to-server: the Next.js backend authenticates with the BFF token and
		// names the user it has signed in. No cookies, so no CSRF exposure.
		if bearer := r.Header.Get(ServiceTokenHeader); bearer != "" {
			if len(s.BFF) == 0 || !hmac.Equal([]byte(bearer), s.BFF) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid service token"})
				return
			}
			u, ok := s.user(r.Header.Get("X-Aegis-User"))
			if !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown user"})
				return
			}
			next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
			return
		}
		u, sess, ok := s.session(r)
		if !ok {
			if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/events" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in required"})
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			tok := r.Header.Get("X-CSRF-Token")
			if tok == "" {
				tok = r.FormValue("csrf")
			}
			if !hmac.Equal([]byte(tok), []byte(s.csrf(sess))) {
				http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
				return
			}
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, u)
		ctx = context.WithValue(ctx, csrfKey{}, s.csrf(sess))
		next(w, r.WithContext(ctx))
	}
}

// service allows only the BFF token, without a user (e.g. the sign-in picker).
func (s *Server) service(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bearer := r.Header.Get(ServiceTokenHeader)
		if bearer == "" || len(s.BFF) == 0 || !hmac.Equal([]byte(bearer), s.BFF) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid service token"})
			return
		}
		next(w, r)
	}
}

// ServiceTokenHeader carries the Next.js backend's service token. A custom header,
// not Authorization, because CloudFront forwards custom headers to the origin.
const ServiceTokenHeader = "X-Aegis-Service-Token"

type csrfKey struct{}

func csrfFrom(r *http.Request) string { v, _ := r.Context().Value(csrfKey{}).(string); return v }
