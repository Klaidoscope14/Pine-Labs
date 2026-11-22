package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aegis/internal/config"
	"aegis/internal/ledger"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{API: config.API{Users: []config.APIUser{
		{ID: "priya", Name: "Priya Sharma", Tenant: "t1", Roles: []string{"requester"}},
		{ID: "arjun", Name: "Arjun Mehta", Tenant: "t1", Roles: []string{"reviewer"}},
	}}}
	s, err := New(cfg, nil, nil, "", []byte(strings.Repeat("k", 32)), nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func cookieFor(t *testing.T, s *Server, id string) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	s.setSession(w, id)
	return w.Result().Cookies()[0]
}

func TestSessionRejectsTampering(t *testing.T) {
	s := testServer(t)
	c := cookieFor(t, s, "priya")
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	if u, _, ok := s.session(r); !ok || u.ID != "priya" {
		t.Fatalf("valid session rejected: %v %v", u, ok)
	}
	// Swap the user inside the cookie but keep the old signature.
	b64, sig, _ := strings.Cut(c.Value, ".")
	_ = b64
	forged := &http.Cookie{Name: cookieName, Value: "YXJqdW58OTk5OTk5OTk5OQ." + sig} // "arjun|9999999999"
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(forged)
	if _, _, ok := s.session(r); ok {
		t.Fatal("forged session accepted")
	}
	// A different key can't validate the cookie either.
	other, _ := New(s.Cfg, nil, nil, "", []byte(strings.Repeat("x", 32)), nil)
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	if _, _, ok := other.session(r); ok {
		t.Fatal("cookie valid under another key")
	}
}

func TestUnsafeRequestsNeedCSRF(t *testing.T) {
	s := testServer(t)
	c := cookieFor(t, s, "priya")
	h := s.auth(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })

	r := httptest.NewRequest("POST", "/runs", strings.NewReader("requirement=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF: %d, want 403", w.Code)
	}

	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	_, sess, _ := s.session(r)
	r = httptest.NewRequest("POST", "/api/runs", strings.NewReader("{}"))
	r.Header.Set("X-CSRF-Token", s.csrf(sess))
	r.AddCookie(c)
	w = httptest.NewRecorder()
	h(w, r)
	if w.Code != 204 {
		t.Fatalf("POST with CSRF: %d, want 204", w.Code)
	}

	// No session: pages redirect to sign-in, the API says 401.
	w = httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/runs", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("page without session: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/api/runs", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("api without session: %d", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	s.secure(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("headers: %v", w.Header())
	}
}

func TestMayDecideMirrorsMakerChecker(t *testing.T) {
	s := testServer(t)
	p := &runPage{Submitter: "priya", Approvals: []ledger.Approval{{Reviewer: "kavya", Decision: "APPROVE"}}}
	if ok, why := s.mayDecide("priya", []string{"requester", "reviewer"}, p); ok || !strings.Contains(why, "submitted") {
		t.Fatalf("submitter allowed: %v %q", ok, why)
	}
	if ok, why := s.mayDecide("kavya", []string{"reviewer"}, p); ok || !strings.Contains(why, "approved") {
		t.Fatalf("second approval by same reviewer allowed: %v %q", ok, why)
	}
	if ok, _ := s.mayDecide("arjun", []string{"reviewer"}, p); !ok {
		t.Fatal("independent reviewer refused")
	}
	if ok, _ := s.mayDecide("rohan", []string{"operator"}, p); ok {
		t.Fatal("non-reviewer allowed")
	}
}

func TestStepsAndTitles(t *testing.T) {
	st := steps("GATES_TIER1", []string{"ANALYZING", "ARCHITECTING", "DEVELOPING", "GATES_TIER1"})
	if st[2].State != "done" || st[3].State != "now" || st[4].State != "todo" {
		t.Fatalf("steps %+v", st)
	}
	st = steps("HALTED_RETRY_EXHAUSTED", []string{"ANALYZING", "ARCHITECTING", "DEVELOPING", "GATES_TIER1"})
	if st[3].State != "fail" {
		t.Fatalf("failed run should mark the step where it stopped: %+v", st)
	}
	if got := title("Requirement: The bank retries settlement webhooks, so duplicates double-credit. Make it idempotent."); got != "The bank retries settlement webhooks" {
		t.Fatalf("title %q", got)
	}
	if a := activityOf([]ledger.Event{{Kind: "run.transition", Payload: map[string]any{"status": "GATES_TIER1"}, CreatedAt: time.Now()},
		{Kind: "run.transition", Payload: map[string]any{"status": "GATES_SECURITY"}, CreatedAt: time.Now()}}); len(a) != 1 {
		t.Fatalf("repeated 'Checking' not collapsed: %+v", a)
	}
}

func TestBFFTokenAndOriginCheck(t *testing.T) {
	s := testServer(t)
	s.BFF = []byte(strings.Repeat("b", 32))
	s.OriginSecret = []byte(strings.Repeat("o", 32))
	h := s.secure(s.auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(userFrom(r).ID))
	}))
	call := func(remote, token, user, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/me", nil)
		r.RemoteAddr = remote
		if token != "" {
			r.Header.Set(ServiceTokenHeader, token)
		}
		if user != "" {
			r.Header.Set("X-Aegis-User", user)
		}
		if origin != "" {
			r.Header.Set("X-Origin-Verify", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	good, origin := string(s.BFF), string(s.OriginSecret)
	if w := call("203.0.113.9:4000", good, "arjun", origin); w.Code != 200 || w.Body.String() != "arjun" {
		t.Fatalf("valid BFF call via CloudFront: %d %q", w.Code, w.Body.String())
	}
	if w := call("203.0.113.9:4000", good, "arjun", ""); w.Code != 403 {
		t.Fatalf("off-host call without the CloudFront header: %d, want 403", w.Code)
	}
	if w := call("203.0.113.9:4000", "wrong-token-wrong-token-wrong-tok", "arjun", origin); w.Code != 401 {
		t.Fatalf("wrong service token: %d, want 401", w.Code)
	}
	if w := call("203.0.113.9:4000", good, "mallory", origin); w.Code != 401 {
		t.Fatalf("unknown user: %d, want 401", w.Code)
	}
	if w := call("127.0.0.1:5000", good, "priya", ""); w.Code != 200 {
		t.Fatalf("loopback (SSM tunnel) needs no CloudFront header: %d", w.Code)
	}
}
