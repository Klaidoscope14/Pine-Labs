package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"aegis/internal/config"
	"aegis/internal/gaterun"
	"aegis/internal/ledger"
)

// ---- status vocabulary: every status is one coloured dot and one word ----

type statusInfo struct {
	Label string // one or two words
	Tone  string // css tone: active, review, ok, bad, muted, wait
}

var statuses = map[string]statusInfo{
	"QUEUED": {"Queued", "wait"}, "PREPARING": {"Starting", "active"}, "ANALYZING": {"Analysing", "active"},
	"ARCHITECTING": {"Planning", "active"}, "WAITING_SCOPE_LEASE": {"Waiting", "wait"}, "ARBITRATING": {"Re-planning", "active"},
	"DEVELOPING": {"Building", "active"}, "PAUSED_PROVIDER": {"Paused", "wait"},
	"GATES_TIER1": {"Checking", "active"}, "GATES_SECURITY": {"Checking", "active"}, "GATES_TIER2": {"Checking", "active"},
	"REBASING": {"Rebasing", "active"}, "PACKAGING": {"Packaging", "active"}, "QUEUED_REVIEW_CAPACITY": {"Queued", "wait"},
	"AWAITING_HUMAN": {"Review", "review"}, "APPROVED": {"Approved", "ok"}, "REJECTED": {"Rejected", "muted"},
	"APPROVAL_EXPIRED": {"Expired", "muted"}, "HALTED_RETRY_EXHAUSTED": {"Failed", "bad"},
	"BLOCKED_SCOPE": {"Blocked", "bad"}, "BLOCKED_SECURITY_VETO": {"Blocked", "bad"}, "ESCALATED_HUMAN": {"Needs human", "bad"},
	"FAILED_SCHEMA": {"Failed", "bad"}, "KILLED": {"Stopped", "muted"}, "BUDGET_EXCEEDED": {"Over budget", "bad"},
	"AGENT_REFUSED": {"Refused", "bad"}, "PROVIDER_ACCESS_DENIED": {"No access", "bad"},
	"MERGED": {"Merged", "ok"}, "MERGE_FAILED": {"Merge failed", "bad"},
}

func status(s string) statusInfo {
	if v, ok := statuses[s]; ok {
		return v
	}
	return statusInfo{strings.Title(strings.ToLower(strings.ReplaceAll(s, "_", " "))), "muted"} //nolint:staticcheck
}

var (
	reviewStatuses = []string{"AWAITING_HUMAN"}
	doneStatuses   = []string{"APPROVED", "REJECTED", "APPROVAL_EXPIRED", "HALTED_RETRY_EXHAUSTED", "BLOCKED_SCOPE",
		"BLOCKED_SECURITY_VETO", "ESCALATED_HUMAN", "FAILED_SCHEMA", "KILLED", "BUDGET_EXCEEDED", "AGENT_REFUSED", "PROVIDER_ACCESS_DENIED", "MERGED", "MERGE_FAILED"}
)

// ---- the five-step strip ----

type step struct {
	Name  string
	State string // done, now, fail, todo
}

var stepOf = map[string]int{
	"QUEUED": 0, "PREPARING": 0, "ANALYZING": 0,
	"ARCHITECTING": 1, "WAITING_SCOPE_LEASE": 1, "ARBITRATING": 1,
	"DEVELOPING": 2, "PAUSED_PROVIDER": 2,
	"GATES_TIER1": 3, "GATES_SECURITY": 3, "GATES_TIER2": 3, "REBASING": 3, "PACKAGING": 3,
	"QUEUED_REVIEW_CAPACITY": 4, "AWAITING_HUMAN": 4,
}

func steps(current string, history []string) []step {
	names := []string{"Analyse", "Plan", "Build", "Checks", "Review"}
	reached := 0
	for _, h := range history {
		if i, ok := stepOf[h]; ok && i > reached {
			reached = i
		}
	}
	out := make([]step, len(names))
	tone := status(current).Tone
	for i, n := range names {
		st := "todo"
		switch {
		case current == "APPROVED" || current == "REJECTED" || current == "MERGED" || current == "MERGE_FAILED":
			st = "done"
		case i < reached:
			st = "done"
		case i == reached && (tone == "bad" || tone == "muted"):
			st = "fail"
		case i == reached:
			st = "now"
		}
		out[i] = step{n, st}
	}
	return out
}

// ---- view models ----

type userView struct {
	config.APIUser
	Initials string
}

func viewUser(u config.APIUser) userView {
	ini := ""
	for _, p := range strings.Fields(u.Name) {
		ini += strings.ToUpper(p[:1])
	}
	if len(ini) > 2 {
		ini = ini[:2]
	}
	return userView{u, ini}
}

type runRow struct {
	ID, Title, Status, Tone, Tier, Cost, Age string
}

func title(requirement string) string {
	t := strings.TrimSpace(requirement)
	t = strings.TrimPrefix(strings.TrimPrefix(t, "# "), "Requirement: ")
	// First sentence or line only.
	for _, sep := range []string{"\n", ". ", ", so ", "; "} {
		if i := strings.Index(t, sep); i > 20 {
			t = t[:i]
		}
	}
	t = strings.TrimSuffix(t, ".")
	if len(t) > 72 {
		cut := strings.LastIndex(t[:72], " ")
		if cut < 40 {
			cut = 72
		}
		t = t[:cut] + "…"
	}
	if t == "" {
		t = "Untitled change"
	}
	return t
}

func money(c *float64) string {
	if c == nil {
		return "—"
	}
	if *c < 0.01 {
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", *c)
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func rows(in []ledger.RunRow) []runRow {
	out := make([]runRow, 0, len(in))
	for _, r := range in {
		st := status(r.Status)
		out = append(out, runRow{ID: r.RunID, Title: title(r.Requirement), Status: st.Label, Tone: st.Tone,
			Tier: r.RiskTier, Cost: money(r.CostUSD), Age: ago(r.CreatedAt)})
	}
	return out
}

// ---- artifacts written by the worker (same host) ----

func (s *Server) runDir(id string) string {
	return filepath.Join(s.Cfg.Path(s.Cfg.Workspace.ArtifactsDir), "runs", filepath.Base(id))
}

type packet struct {
	Title   string   `json:"pr_title"`
	Summary string   `json:"summary"`
	Risks   []string `json:"risks"`
}

func (s *Server) packet(id string) (packet, bool) {
	var p packet
	data, err := os.ReadFile(filepath.Join(s.runDir(id), "release_packet.json"))
	if err != nil || json.Unmarshal(data, &p) != nil {
		return p, false
	}
	return p, true
}

// ---- diff ----

type diffLine struct {
	Kind, Text string // add, del, hunk, ctx
}

type diffFile struct {
	Path      string
	Add, Del  int
	Lines     []diffLine
	Truncated bool
}

const maxDiffBytes = 400 << 10

func (s *Server) diff(id string) []diffFile {
	data, err := os.ReadFile(filepath.Join(s.runDir(id), "final.diff"))
	if err != nil {
		return nil
	}
	if len(data) > maxDiffBytes {
		data = data[:maxDiffBytes]
	}
	var files []diffFile
	var cur *diffFile
	for _, l := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			files = append(files, diffFile{})
			cur = &files[len(files)-1]
			if i := strings.LastIndex(l, " b/"); i > 0 {
				cur.Path = l[i+3:]
			}
		case cur == nil, strings.HasPrefix(l, "index "), strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "+++ "),
			strings.HasPrefix(l, "new file mode"), strings.HasPrefix(l, "deleted file mode"):
		case strings.HasPrefix(l, "@@"):
			cur.Lines = append(cur.Lines, diffLine{"hunk", l})
		case strings.HasPrefix(l, "+"):
			cur.Add++
			cur.Lines = append(cur.Lines, diffLine{"add", l[1:]})
		case strings.HasPrefix(l, "-"):
			cur.Del++
			cur.Lines = append(cur.Lines, diffLine{"del", l[1:]})
		default:
			cur.Lines = append(cur.Lines, diffLine{"ctx", strings.TrimPrefix(l, " ")})
		}
	}
	for i := range files {
		if len(files[i].Lines) > 800 {
			files[i].Lines, files[i].Truncated = files[i].Lines[:800], true
		}
	}
	return files
}

// ---- checks: the latest attempt's gate outcomes ----

type gateView struct {
	Name, Verdict, Tone, Summary string
	Findings                     []findingView
	Reused                       bool
}

type findingView struct {
	Rule, Where, Message, Tone string
}

var gateNames = map[string]string{
	"scope": "Scope", "integrity": "Test integrity", "lint": "Lint", "tests": "Tests", "hidden_tests": "Hidden tests",
	"semgrep": "Fintech rules", "bandit": "Security lint", "secrets": "Secrets", "dependencies": "Dependencies",
	"openapi": "API compatibility", "replay": "Replay",
}

func verdictTone(v string) string {
	switch v {
	case "PASS":
		return "ok"
	case "FLAG":
		return "wait"
	case "REMEDIABLE_FAIL":
		return "bad"
	case "HARD_BLOCK":
		return "bad"
	}
	return "muted"
}

func verdictLabel(v string) string {
	return map[string]string{"PASS": "Pass", "FLAG": "Flag", "REMEDIABLE_FAIL": "Fail", "HARD_BLOCK": "Block"}[v]
}

func (s *Server) checks(id string) ([]gateView, int) {
	dir := s.runDir(id)
	entries, _ := os.ReadDir(dir)
	attempt := 0
	for _, e := range entries {
		if n, err := strconv.Atoi(strings.TrimPrefix(e.Name(), "attempt-")); err == nil && strings.HasPrefix(e.Name(), "attempt-") && n > attempt {
			attempt = n
		}
	}
	if attempt == 0 {
		return nil, 0
	}
	var out []gateView
	for _, tier := range []string{"gates_tier1", "gates_security", "gates_tier2"} {
		data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("attempt-%d", attempt), tier, "outcome.json"))
		if err != nil {
			continue
		}
		var o gaterun.Outcome
		if json.Unmarshal(data, &o) != nil {
			continue
		}
		for _, r := range o.Results {
			g := gateView{Name: gateNames[r.Gate], Verdict: verdictLabel(string(r.Verdict)), Tone: verdictTone(string(r.Verdict)),
				Summary: r.Summary, Reused: r.Metrics["reused"] == 1}
			if g.Name == "" {
				g.Name = r.Gate
			}
			for _, f := range r.Findings {
				if f.PreExisting {
					continue
				}
				where := f.File
				if f.Line > 0 {
					where = fmt.Sprintf("%s:%d", f.File, f.Line)
				}
				g.Findings = append(g.Findings, findingView{Rule: f.RuleID, Where: where, Message: f.Message, Tone: verdictTone(string(f.Verdict))})
			}
			out = append(out, g)
		}
	}
	// Failing gates first, then by name.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Tone == "ok") != (out[j].Tone == "ok") {
			return out[j].Tone == "ok"
		}
		return false
	})
	return out, attempt
}

// ---- activity: audit events as short sentences ----

type activity struct {
	Text, When, Tone string
}

func activityOf(events []ledger.Event) []activity {
	var out []activity
	lastStatus := ""
	for _, e := range events {
		p := e.Payload
		str := func(k string) string { v, _ := p[k].(string); return v }
		text, tone := "", "muted"
		switch e.Kind {
		case "run.created":
			text = "Created"
			if sub := str("submitter"); sub != "" {
				text = "Created by " + sub
			}
		case "run.transition":
			st := str("status")
			if st == lastStatus || st == "" {
				continue
			}
			lastStatus = st
			info := status(st)
			text, tone = info.Label, info.Tone
			if n, ok := p["attempt"].(float64); ok && n > 1 && st == "DEVELOPING" {
				text = fmt.Sprintf("Building (attempt %d)", int(n))
			}
		case "gates.evaluated":
			n, _ := p["new_findings"].(float64)
			text = "Checks ran"
			if n > 0 {
				text = fmt.Sprintf("Checks ran · %d finding(s)", int(n))
			}
		case "human.decision":
			if str("decision") == "APPROVE" {
				text, tone = "Approved by "+str("reviewer"), "ok"
			} else {
				text = "Rejected by " + str("reviewer")
			}
		case "human.decision_refused":
			text, tone = "Refused "+str("reviewer")+": "+str("reason"), "bad"
		case "scope.lease_acquired":
			text = "Reserved planned files"
		case "scope.lease_released":
			text = "Released planned files"
		case "run.base_moved":
			text = "Main moved; rebased"
		case "run.reconciled":
			text = "Status corrected: " + str("to")
		case "run.merge_queued":
			text = "Queued to merge"
		case "run.merge":
			if str("status") == "MERGED" {
				text, tone = "Merged into main", "ok"
			} else {
				text, tone = "Merge failed: "+str("reason"), "bad"
			}
		case "policy.decided":
			continue
		case "human.passkey_assertion":
			text, tone = "Signed with passkey by "+str("reviewer"), "ok"
		default:
			continue
		}
		if len(out) > 0 && out[len(out)-1].Text == text {
			continue // e.g. three gate tiers all read "Checking"
		}
		out = append(out, activity{Text: text, When: e.CreatedAt.Format("15:04:05"), Tone: tone})
	}
	return out
}

// ---- template helpers ----

var funcs = template.FuncMap{
	"short": func(s string) string {
		if len(s) > 8 {
			return s[:8]
		}
		return s
	},
	"has": func(roles []string, r string) bool { return slices.Contains(roles, r) },
	"json": func(v any) template.JS {
		b, _ := json.Marshal(v)
		return template.JS(b) //nolint:gosec // marshalled JSON, emitted into a non-executed JSON block
	},
}
