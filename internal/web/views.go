package web

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"aegis/internal/ops"
)

// JSON views for the Next.js frontend: the same data the Go pages render.

type meView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Initials    string   `json:"initials"`
	Tenant      string   `json:"tenant"`
	Roles       []string `json:"roles"`
	ReviewCount int      `json:"reviewCount"`
	Killed      string   `json:"killed"`
	Repos       []string `json:"repos"`
	Scenarios   bool     `json:"scenarios"`
	Passkeys    int      `json:"passkeys"`
	NeedPasskey bool     `json:"needPasskey"`
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	b := s.base(r, "", "")
	m := meView{ID: u.ID, Name: u.Name, Initials: b.User.Initials, Tenant: u.Tenant, Roles: u.Roles,
		ReviewCount: b.ReviewCount, Killed: b.Killed, Scenarios: s.Cfg.Deployment.Environment != "production",
		NeedPasskey: s.Cfg.Approval.RequirePasskey}
	for _, rp := range s.Cfg.Repos {
		m.Repos = append(m.Repos, rp.Name)
	}
	if keys, err := s.Store.Passkeys(r.Context(), u.Tenant, u.ID); err == nil {
		m.Passkeys = len(keys)
	}
	writeJSON(w, 200, m)
}

type userItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Initials string `json:"initials"`
	Tenant   string `json:"tenant"`
	Roles    string `json:"roles"`
}

func (s *Server) apiUsers(w http.ResponseWriter, _ *http.Request) {
	out := []userItem{}
	for _, u := range s.Cfg.API.Users {
		out = append(out, userItem{u.ID, u.Name, viewUser(u).Initials, u.Tenant, strings.Join(u.Roles, " · ")})
	}
	writeJSON(w, 200, out)
}

func (s *Server) apiRunsView(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	view := r.URL.Query().Get("view")
	var statuses []string
	exclude := false
	switch view {
	case "review":
		statuses = reviewStatuses
	case "done":
		statuses = doneStatuses
	default:
		view, statuses, exclude = "active", append(slices.Clone(doneStatuses), reviewStatuses...), true
	}
	list, err := s.Store.ListRuns(r.Context(), u.Tenant, statuses, exclude, 100)
	if err != nil {
		apiErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"view": view, "rows": rows(list)})
}

func (s *Server) apiRunView(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id := r.PathValue("id")
	p, err := s.loadRun(r.Context(), u.Tenant, id)
	if err != nil {
		apiErr(w, 404, errors.New("no such run"))
		return
	}
	switch r.URL.Query().Get("tab") {
	case "changes":
		p.Diff = s.diff(id)
	case "checks":
		p.Checks, p.Attempt = s.checks(id)
	case "activity":
		events, _ := s.Store.RunEvents(r.Context(), u.Tenant, id)
		p.Activity = activityOf(events)
	default:
		p.Files = len(s.diff(id))
	}
	if p.InReview {
		p.CanDecide, p.Why = s.mayDecide(u.ID, u.Roles, p)
		if keys, err := s.Store.Passkeys(r.Context(), u.Tenant, u.ID); err == nil {
			p.HasPasskey = len(keys) > 0
		}
		p.NeedPasskey = s.Cfg.Approval.RequirePasskey
	}
	p.base = base{} // page chrome is the frontend's job
	writeJSON(w, 200, p)
}

func (s *Server) apiUnkill(w http.ResponseWriter, r *http.Request) {
	u := who(r)
	if !slices.Contains(u.Roles, "operator") {
		apiErr(w, 403, errors.New("only operators can use the kill switch"))
		return
	}
	if err := ops.Unkill(r.Context(), s.AdminDSN, u.Tenant, "re-enabled from the web app", u.ID); err != nil {
		apiErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "off"})
}
