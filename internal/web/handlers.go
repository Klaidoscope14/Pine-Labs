package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"aegis/internal/app"
	"aegis/internal/ledger"
	"aegis/internal/ops"
	"aegis/internal/workflow"
)

// base is what every page needs: who is signed in and the sidebar counts.
type base struct {
	User        userView
	CSRF        string
	Nav         string
	ReviewCount int
	Killed      string
	PageTitle   string
}

func (s *Server) base(r *http.Request, nav, title string) base {
	u := userFrom(r)
	b := base{User: viewUser(u), CSRF: csrfFrom(r), Nav: nav, PageTitle: title}
	b.ReviewCount, _ = s.Store.CountRuns(r.Context(), u.Tenant, reviewStatuses)
	if on, reason, err := s.Store.KillActive(r.Context(), u.Tenant); err == nil && on {
		b.Killed = reason
	}
	return b
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl[page].ExecuteTemplate(w, "layout.html", data); err != nil {
		s.Log.Error("render", "page", page, "error", err.Error())
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, code int, msg string) {
	w.WriteHeader(code)
	s.render(w, "error", struct {
		base
		Message string
	}{s.base(r, "", "Error"), msg})
}

// ---- sign in ----

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	type person struct {
		ID, Name, Initials, Role, Tenant string
	}
	var people []person
	for _, u := range s.Cfg.API.Users {
		people = append(people, person{u.ID, u.Name, viewUser(u).Initials, strings.Join(u.Roles, " · "), u.Tenant})
	}
	s.render(w, "login", struct {
		base
		People []person
	}{base{PageTitle: "Sign in"}, people})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.user(r.FormValue("user")); !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.setSession(w, r.FormValue("user"))
	http.Redirect(w, r, "/runs", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---- runs list ----

func (s *Server) runsPage(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	view := r.URL.Query().Get("view")
	var list []ledger.RunRow
	var err error
	switch view {
	case "review":
		list, err = s.Store.ListRuns(r.Context(), u.Tenant, reviewStatuses, false, 100)
	case "done":
		list, err = s.Store.ListRuns(r.Context(), u.Tenant, doneStatuses, false, 100)
	default:
		view = "active"
		list, err = s.Store.ListRuns(r.Context(), u.Tenant, append(slices.Clone(doneStatuses), reviewStatuses...), true, 100)
	}
	if err != nil {
		s.fail(w, r, 500, "Could not load runs.")
		return
	}
	nav := "runs"
	if view == "review" {
		nav = "review"
	}
	s.render(w, "runs", struct {
		base
		View string
		Rows []runRow
	}{s.base(r, nav, "Runs"), view, rows(list)})
}

// ---- one run ----

type runPage struct {
	base
	ID, Title, Status, Tone, Tier, Submitter, Cost, Age, Tab string
	Steps                                                    []step
	Packet                                                   packet
	HasPacket                                                bool
	Diff                                                     []diffFile
	Checks                                                   []gateView
	Attempt                                                  int
	Activity                                                 []activity
	Approvals                                                []ledger.Approval
	Need, Have                                               int
	InReview, CanDecide, HasPasskey, NeedPasskey             bool
	Why                                                      string
	Calls                                                    int
	Tokens                                                   string
	Files                                                    int
}

func (s *Server) loadRun(ctx context.Context, tenant, id string) (*runPage, error) {
	meta, err := s.Store.RunMeta(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	sum, err := s.Store.Summary(ctx, tenant, id)
	if err != nil {
		return nil, err
	}
	var history []string
	for _, st := range sum.Steps {
		if v, ok := st["status"].(string); ok {
			history = append(history, v)
		}
	}
	info := status(meta.Status)
	p := &runPage{ID: id, Title: title(meta.Requirement), Status: info.Label, Tone: info.Tone, Tier: meta.RiskTier,
		Submitter: meta.Submitter, Age: ago(meta.CreatedAt), Steps: steps(meta.Status, history),
		InReview: meta.Status == "AWAITING_HUMAN"}
	p.Packet, p.HasPacket = s.packet(id)
	if p.HasPacket && p.Packet.Title != "" {
		p.Title = title(p.Packet.Title)
	}
	if usage, err := s.Store.RunUsage(ctx, tenant, id); err == nil {
		p.Cost, p.Calls = money(usage.CostUSD), usage.Calls
		p.Tokens = fmt.Sprintf("%dk", (usage.InputTokens+usage.OutputTokens+usage.CacheRead)/1000)
	}
	p.Approvals, _ = s.Store.Approvals(ctx, tenant, id)
	p.Need = 1
	if n := s.Cfg.Review.RequiredApprovers[meta.RiskTier]; n > 1 {
		p.Need = n
	}
	for _, a := range p.Approvals {
		if a.Decision == "APPROVE" {
			p.Have++
		}
	}
	return p, nil
}

func (s *Server) runPage(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id := r.PathValue("id")
	p, err := s.loadRun(r.Context(), u.Tenant, id)
	if err != nil {
		// Just submitted: the workflow exists but its worker hasn't written the
		// ledger row yet. Show it as queued; the page refreshes when it appears.
		if d, derr := s.Temporal.DescribeWorkflowExecution(r.Context(), id, ""); derr == nil &&
			d.WorkflowExecutionInfo.GetStatus() == enums.WORKFLOW_EXECUTION_STATUS_RUNNING && strings.HasPrefix(id, "intake-") {
			q := status("QUEUED")
			p = &runPage{ID: id, Title: "Starting…", Status: q.Label, Tone: q.Tone, Steps: steps("QUEUED", nil), Tab: "overview"}
			p.base = s.base(r, "runs", "Starting")
			s.render(w, "run", p)
			return
		}
		s.fail(w, r, http.StatusNotFound, "No such run.")
		return
	}
	p.base = s.base(r, "runs", p.Title)
	p.Tab = r.URL.Query().Get("tab")
	switch p.Tab {
	case "changes":
		p.Diff = s.diff(id)
	case "checks":
		p.Checks, p.Attempt = s.checks(id)
	case "activity":
		events, _ := s.Store.RunEvents(r.Context(), u.Tenant, id)
		p.Activity = activityOf(events)
	default:
		p.Tab = "overview"
		p.Files = len(s.diff(id))
	}
	if p.InReview {
		p.CanDecide, p.Why = s.mayDecide(u.ID, u.Roles, p)
		if keys, err := s.Store.Passkeys(r.Context(), u.Tenant, u.ID); err == nil {
			p.HasPasskey = len(keys) > 0
		}
		p.NeedPasskey = s.Cfg.Approval.RequirePasskey
	}
	s.render(w, "run", p)
}

// mayDecide mirrors the workflow's maker-checker so the UI explains, up front,
// why Approve isn't offered. The workflow enforces it regardless.
func (s *Server) mayDecide(user string, roles []string, p *runPage) (bool, string) {
	if !slices.Contains(roles, "reviewer") {
		return false, "Only reviewers can decide."
	}
	if p.Submitter == user {
		return false, "You submitted this run. Another reviewer must approve it."
	}
	for _, a := range p.Approvals {
		if a.Reviewer == user && a.Decision == "APPROVE" {
			return false, "You approved this. Waiting for another reviewer."
		}
	}
	return true, ""
}

// ---- new run ----

func (s *Server) newPage(w http.ResponseWriter, r *http.Request) {
	var repos []string
	for _, rp := range s.Cfg.Repos {
		repos = append(repos, rp.Name)
	}
	s.render(w, "new", struct {
		base
		Repos    []string
		Scenario bool
	}{s.base(r, "new", "New run"), repos, s.Cfg.Deployment.Environment != "production"})
}

type createReq struct {
	Requirement string `json:"requirement"`
	Repo        string `json:"repo"`
	Scenario    string `json:"scenario,omitempty"`
}

const maxRequirement = 8000

// startRun is idempotent intake: the run id is the intake key, so submitting the
// same ticket twice attaches to the run already in progress.
func (s *Server) startRun(ctx context.Context, u userViewOrUser, req createReq) (string, error) {
	req.Requirement = strings.TrimSpace(req.Requirement)
	if req.Requirement == "" {
		return "", errors.New("describe the change")
	}
	if len(req.Requirement) > maxRequirement {
		return "", fmt.Errorf("keep the ticket under %d characters", maxRequirement)
	}
	if !slices.Contains(u.Roles, "requester") {
		return "", errors.New("only requesters can start runs")
	}
	repo := ""
	for _, rp := range s.Cfg.Repos {
		if rp.Name == req.Repo || (req.Repo == "" && repo == "") {
			repo = rp.Name
		}
	}
	if repo == "" {
		return "", errors.New("unknown repository")
	}
	if req.Scenario != "" && s.Cfg.Deployment.Environment == "production" {
		return "", errors.New("scenarios are for testing only")
	}
	if on, reason, err := s.Store.KillActive(ctx, u.Tenant); err != nil {
		return "", err
	} else if on {
		return "", errors.New("the kill switch is on: " + reason)
	}
	id := app.IntakeKey(u.Tenant, repo, req.Requirement)
	in := workflow.Input{RunID: id, TenantID: u.Tenant, Repo: repo, Requirement: req.Requirement, Scenario: req.Scenario,
		Submitter: u.ID, ConfigHash: s.Cfg.Hash, Settings: app.Settings(s.Cfg, u.Tenant)}
	if _, err := s.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: app.TaskQueue(s.Cfg),
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING}, app.WorkflowName, in); err != nil {
		return "", err
	}
	// Give the worker a moment to record the run, so the next page has it.
	for i := 0; i < 20; i++ {
		if _, err := s.Store.RunMeta(ctx, u.Tenant, id); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return id, nil
}

type userViewOrUser = struct {
	ID, Tenant string
	Roles      []string
}

func who(r *http.Request) userViewOrUser {
	u := userFrom(r)
	return userViewOrUser{u.ID, u.Tenant, u.Roles}
}

func (s *Server) createRunForm(w http.ResponseWriter, r *http.Request) {
	id, err := s.startRun(r.Context(), who(r), createReq{Requirement: r.FormValue("requirement"), Repo: r.FormValue("repo"),
		Scenario: r.FormValue("scenario")})
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	http.Redirect(w, r, "/runs/"+id, http.StatusSeeOther)
}

// ---- decisions ----

func (s *Server) decide(ctx context.Context, u userViewOrUser, id string, approve bool, note string, passkey bool, cred string) error {
	meta, err := s.Store.RunMeta(ctx, u.Tenant, id)
	if err != nil {
		return errors.New("no such run")
	}
	if meta.Status != "AWAITING_HUMAN" {
		return errors.New("this run is not waiting for a decision")
	}
	if !slices.Contains(u.Roles, "reviewer") {
		return errors.New("only reviewers can decide")
	}
	if approve && meta.Submitter == u.ID {
		return errors.New("you submitted this run; another reviewer must approve it")
	}
	return s.Temporal.SignalWorkflow(ctx, id, "", workflow.SignalDecision,
		workflow.Decision{Approve: approve, Reviewer: u.ID, Note: strings.TrimSpace(note), Passkey: passkey, Credential: cred})
}

func (s *Server) decisionForm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.decide(r.Context(), who(r), id, r.FormValue("decision") == "approve", r.FormValue("note"), false, ""); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	time.Sleep(700 * time.Millisecond) // let the workflow record it, so the page shows the result
	http.Redirect(w, r, "/runs/"+id, http.StatusSeeOther)
}

// ---- settings and kill switch ----

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var others []userView
	for _, o := range s.Cfg.API.Users {
		if o.ID != u.ID {
			others = append(others, viewUser(o))
		}
	}
	keys, _ := s.Store.Passkeys(r.Context(), u.Tenant, u.ID)
	s.render(w, "settings", struct {
		base
		Operator bool
		Others   []userView
		Passkeys int
	}{s.base(r, "settings", "Settings"), slices.Contains(u.Roles, "operator"), others, len(keys)})
}

func (s *Server) kill(ctx context.Context, u userViewOrUser, reason string) error {
	if !slices.Contains(u.Roles, "operator") {
		return errors.New("only operators can use the kill switch")
	}
	if strings.TrimSpace(reason) == "" {
		return errors.New("give a reason")
	}
	kctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	log, err := ops.Kill(kctx, s.AdminDSN, s.Temporal, s.Store, u.Tenant, strings.TrimSpace(reason), u.ID)
	for _, l := range log {
		s.Log.Info("kill", "line", l)
	}
	return err
}

func (s *Server) killForm(w http.ResponseWriter, r *http.Request) {
	if err := s.kill(r.Context(), who(r), r.FormValue("reason")); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) unkillForm(w http.ResponseWriter, r *http.Request) {
	u := who(r)
	if !slices.Contains(u.Roles, "operator") {
		s.fail(w, r, http.StatusForbidden, "Only operators can use the kill switch.")
		return
	}
	if err := ops.Unkill(r.Context(), s.AdminDSN, u.Tenant, "re-enabled from the web app", u.ID); err != nil {
		s.fail(w, r, 500, err.Error())
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// ---- live updates (server-sent events) ----

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	u := userFrom(r)
	last, _ := s.Store.LastChange(r.Context(), u.Tenant)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	deadline := time.After(10 * time.Minute) // the browser reconnects
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline:
			return
		case <-tick.C:
			t, err := s.Store.LastChange(r.Context(), u.Tenant)
			if err == nil && t.After(last) {
				last = t
				fmt.Fprintf(w, "event: change\ndata: %d\n\n", t.UnixMilli())
				fl.Flush()
			}
		}
	}
}

// ---- JSON API ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) apiListRuns(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListRuns(r.Context(), userFrom(r).Tenant, nil, false, 200)
	if err != nil {
		apiErr(w, 500, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) apiGetRun(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id := r.PathValue("id")
	meta, err := s.Store.RunMeta(r.Context(), u.Tenant, id)
	if err != nil {
		apiErr(w, 404, errors.New("no such run"))
		return
	}
	approvals, _ := s.Store.Approvals(r.Context(), u.Tenant, id)
	usage, _ := s.Store.RunUsage(r.Context(), u.Tenant, id)
	writeJSON(w, 200, map[string]any{"run": meta, "approvals": approvals, "usage": usage})
}

func (s *Server) apiCreateRun(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		apiErr(w, 400, errors.New("invalid JSON"))
		return
	}
	id, err := s.startRun(r.Context(), who(r), req)
	if err != nil {
		apiErr(w, 400, err)
		return
	}
	writeJSON(w, 202, map[string]string{"run_id": id})
}

func (s *Server) apiDecision(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		apiErr(w, 400, errors.New("invalid JSON"))
		return
	}
	if err := s.decide(r.Context(), who(r), r.PathValue("id"), req.Approve, req.Note, false, ""); err != nil {
		apiErr(w, 400, err)
		return
	}
	writeJSON(w, 202, map[string]string{"status": "recorded"})
}

func (s *Server) apiKill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		apiErr(w, 400, errors.New("invalid JSON"))
		return
	}
	if err := s.kill(r.Context(), who(r), req.Reason); err != nil {
		apiErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "killed"})
}
