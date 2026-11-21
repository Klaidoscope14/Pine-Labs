package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"aegis/internal/agents"
	"aegis/internal/agents/llmagents"
	"aegis/internal/config"
	"aegis/internal/contracts"
	"aegis/internal/dlp"
	"aegis/internal/gaterun"
	"aegis/internal/gates"
	"aegis/internal/ledger"
	"aegis/internal/llm"
	"aegis/internal/sandbox"
	"aegis/internal/workspace"
)

// Activities holds the dependencies of every activity. One instance per worker.
type Activities struct {
	Cfg     *config.Config
	Store   *ledger.Store
	WS      workspace.Manager
	Sandbox sandbox.Provider
	Agents  agents.Agents
	Image   string
}

// ---- activity payloads (kept small: hashes and summaries only) ----

type WorkspaceInfo struct {
	RunID   string `json:"run_id"`
	Mirror  string `json:"mirror"`
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	BaseSHA string `json:"base_sha"`
}

func (w WorkspaceInfo) worktree() workspace.Worktree {
	return workspace.Worktree{RunID: w.RunID, Mirror: w.Mirror, Path: w.Path, Branch: w.Branch, BaseSHA: w.BaseSHA}
}

type TransitionInput struct {
	Run       Input          `json:"run"`
	Seq       int            `json:"seq"`
	Node      string         `json:"node"`
	Status    Status         `json:"status"`
	Attempt   int            `json:"attempt"`
	CommitSHA string         `json:"commit_sha"`
	BaseSHA   string         `json:"base_sha"`
	Branch    string         `json:"branch"`
	Detail    map[string]any `json:"detail"`
}

type AgentInput struct {
	Run       Input           `json:"run"`
	Workspace WorkspaceInfo   `json:"workspace"`
	Prior     json.RawMessage `json:"prior,omitempty"`
}

type AgentOutput struct {
	JSON json.RawMessage `json:"json"`
}

type DevelopActivityInput struct {
	Run         Input           `json:"run"`
	Workspace   WorkspaceInfo   `json:"workspace"`
	Spec        json.RawMessage `json:"spec"`
	Plan        json.RawMessage `json:"plan"`
	Attempt     int             `json:"attempt"`
	Diagnostics string          `json:"diagnostics"`
}

type DevelopResult struct {
	CommitSHA      string `json:"commit_sha"`
	Conflict       bool   `json:"conflict"`
	ConflictReason string `json:"conflict_reason"`
}

type EvalInput struct {
	Run       Input           `json:"run"`
	Workspace WorkspaceInfo   `json:"workspace"`
	Plan      json.RawMessage `json:"plan"`
	HeadSHA   string          `json:"head_sha"`
	Attempt   int             `json:"attempt"`
	Tier      string          `json:"tier"`
	Gates     []string        `json:"gates"`
}

type GateBrief struct {
	Gate    string `json:"gate"`
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`
}

type EvalSummary struct {
	Verdict     string      `json:"verdict"`
	BlockStatus string      `json:"block_status,omitempty"`
	Reason      string      `json:"reason,omitempty"`
	Diagnostics string      `json:"diagnostics,omitempty"`
	Gates       []GateBrief `json:"gates"`
	ArtifactRef string      `json:"artifact_ref"`
}

type ReleaseInput struct {
	Run       Input           `json:"run"`
	Workspace WorkspaceInfo   `json:"workspace"`
	Spec      json.RawMessage `json:"spec"`
	Plan      json.RawMessage `json:"plan"`
	HeadSHA   string          `json:"head_sha"`
	Attempts  int             `json:"attempts"`
}

type ReleaseResult struct {
	EvidenceHash string `json:"evidence_hash"`
	PacketRef    string `json:"packet_ref"`
}

type DecisionInput struct {
	Run          Input    `json:"run"`
	Decision     Decision `json:"decision"`
	HeadSHA      string   `json:"head_sha"`
	EvidenceHash string   `json:"evidence_hash"`
}

// ---- helpers ----

// Application error types the workflow maps to terminal states.
const (
	ErrAgentRefused  = "AgentRefused"
	ErrBudget        = "BudgetExceeded"
	ErrKilled        = "Killed"
	ErrSensitiveData = "SensitiveData"
	ErrNeedsHuman    = "NeedsHuman"
)

// agentError turns an agent failure into the right Temporal error: terminal
// conditions become non-retryable typed errors; provider outages stay retryable.
func agentError(role string, err error) error {
	nr := func(kind string) error {
		return temporal.NewNonRetryableApplicationError(role+": "+err.Error(), kind, err)
	}
	switch {
	case errors.Is(err, llmagents.ErrContract):
		return nr(ErrContract)
	case errors.Is(err, llm.ErrRefused):
		return nr(ErrAgentRefused)
	case errors.Is(err, llm.ErrBudget):
		return nr(ErrBudget)
	case errors.Is(err, llm.ErrKilled):
		return nr(ErrKilled)
	case errors.Is(err, llm.ErrSensitiveData):
		return nr(ErrSensitiveData)
	case errors.Is(err, llm.ErrNoCassette):
		return nr(ErrScenario)
	case errors.Is(err, agents.ErrNeedsHuman):
		return nr(ErrNeedsHuman)
	case errors.Is(err, llm.ErrUnavailable):
		return err // retried by Temporal with backoff
	default:
		return nr(ErrScenario)
	}
}

func (a *Activities) agentRun(in Input, attempt int) agents.Run {
	return agents.Run{TenantID: in.TenantID, RunID: in.RunID, Scenario: in.Scenario, Repo: in.Repo, Attempt: attempt}
}

func contractError(name string, err error) error {
	return temporal.NewNonRetryableApplicationError(fmt.Sprintf("%s failed its contract: %v", name, err), ErrContract, err)
}

func (a *Activities) runDir(runID string) string {
	return filepath.Join(a.Cfg.Path(a.Cfg.Workspace.ArtifactsDir), "runs", runID)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// keepAlive heartbeats for the lifetime of an activity, so a crashed worker is
// detected within the heartbeat timeout. Returns the stop function.
func keepAlive(ctx context.Context) func() {
	timeout := activity.GetInfo(ctx).HeartbeatTimeout
	if timeout <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(timeout / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				activity.RecordHeartbeat(ctx)
			}
		}
	}()
	activity.RecordHeartbeat(ctx)
	return func() { close(done) }
}

// ---- activities ----

func (a *Activities) CreateRun(ctx context.Context, in Input) error {
	defer keepAlive(ctx)()
	if on, reason, err := a.Store.KillActive(ctx, in.TenantID); err != nil {
		return err
	} else if on {
		return temporal.NewNonRetryableApplicationError("kill switch is on: "+reason, ErrKilled, nil)
	}
	// Tickets can contain card numbers or personal data; the ledger only ever keeps the redacted text.
	requirement, _ := dlp.Redact(in.Requirement)
	return a.Store.CreateRun(ctx, ledger.Run{RunID: in.RunID, TenantID: in.TenantID, Scenario: in.Scenario,
		Repo: in.Repo, Requirement: requirement, Status: string(Queued), ConfigHash: in.ConfigHash})
}

func (a *Activities) RecordTransition(ctx context.Context, t TransitionInput) error {
	defer keepAlive(ctx)()
	return a.Store.RecordTransition(ctx, ledger.Transition{TenantID: t.Run.TenantID, RunID: t.Run.RunID, Seq: t.Seq,
		Node: t.Node, Attempt: t.Attempt, Status: string(t.Status), CommitSHA: t.CommitSHA,
		BaseSHA: t.BaseSHA, Branch: t.Branch, Detail: t.Detail})
}

func (a *Activities) Prepare(ctx context.Context, in Input) (WorkspaceInfo, error) {
	defer keepAlive(ctx)()
	repo, ok := a.Cfg.Repo(in.Repo)
	if !ok {
		return WorkspaceInfo{}, temporal.NewNonRetryableApplicationError("unknown repo "+in.Repo, ErrScenario, nil)
	}
	remote, err := a.Cfg.RepoRemote(repo)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	mirror, err := a.WS.EnsureMirror(ctx, remote)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	// The base is pinned once and stored, so a retried Prepare never moves it.
	raw, err := a.Store.Once(ctx, in.TenantID, in.RunID, in.RunID+":base", func() (json.RawMessage, error) {
		ref := in.BaseRef
		if ref == "" {
			ref = repo.DefaultBranch
		}
		sha, err := a.WS.ResolveRef(ctx, mirror, ref)
		if err != nil {
			return nil, err
		}
		return json.Marshal(sha)
	})
	if err != nil {
		return WorkspaceInfo{}, err
	}
	var base string
	if err := json.Unmarshal(raw, &base); err != nil {
		return WorkspaceInfo{}, err
	}
	wt, err := a.WS.CreateWorktree(ctx, mirror, in.RunID, base)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	return WorkspaceInfo{RunID: wt.RunID, Mirror: wt.Mirror, Path: wt.Path, Branch: wt.Branch, BaseSHA: wt.BaseSHA}, nil
}

func (a *Activities) Analyze(ctx context.Context, in AgentInput) (AgentOutput, error) {
	defer keepAlive(ctx)()
	out, err := a.Agents.Analyze(ctx, agents.AnalyzeInput{Run: a.agentRun(in.Run, 0), Requirement: in.Run.Requirement,
		Worktree: in.Workspace.Path})
	if err != nil {
		return AgentOutput{}, agentError("analyst", err)
	}
	if err := contracts.Validate("requirement_spec", out); err != nil {
		return AgentOutput{}, contractError("requirement_spec", err)
	}
	return AgentOutput{JSON: out}, nil
}

func (a *Activities) Architect(ctx context.Context, in AgentInput) (AgentOutput, error) {
	defer keepAlive(ctx)()
	out, err := a.Agents.Architect(ctx, agents.ArchitectInput{Run: a.agentRun(in.Run, 0),
		RequirementSpec: in.Prior, Worktree: in.Workspace.Path})
	if err != nil {
		if errors.Is(err, agents.ErrNeedsHuman) && out != nil {
			// Keep the plan as evidence for the human design decision.
			_ = writeJSON(filepath.Join(a.runDir(in.Run.RunID), "architecture_plan.json"), json.RawMessage(out))
		}
		return AgentOutput{}, agentError("architect", err)
	}
	if err := contracts.Validate("architecture_plan", out); err != nil {
		return AgentOutput{}, contractError("architecture_plan", err)
	}
	return AgentOutput{JSON: out}, nil
}

func (a *Activities) Develop(ctx context.Context, in DevelopActivityInput) (DevelopResult, error) {
	defer keepAlive(ctx)()
	key := fmt.Sprintf("%s:develop:%d", in.Run.RunID, in.Attempt)
	raw, err := a.Store.Once(ctx, in.Run.TenantID, in.Run.RunID, key, func() (json.RawMessage, error) {
		wt := in.Workspace.worktree()
		// Agents always start from the last committed state (attempt 1: the base;
		// later attempts: the previous attempt's commit). Partial edits from a
		// crashed execution are discarded.
		if err := a.WS.ResetClean(ctx, wt); err != nil {
			return nil, err
		}
		out, err := a.Agents.Develop(ctx, agents.DevelopInput{Run: a.agentRun(in.Run, in.Attempt),
			RequirementSpec: in.Spec, Plan: in.Plan, Worktree: in.Workspace.Path, Diagnostics: in.Diagnostics})
		if err != nil {
			return nil, agentError("developer", err)
		}
		if out.Conflict != nil {
			if err := contracts.Validate("developer_result", out.Conflict); err != nil {
				return nil, contractError("developer_result", err)
			}
			var c struct {
				Conflict struct {
					Reason string `json:"reason"`
				} `json:"conflict"`
			}
			_ = json.Unmarshal(out.Conflict, &c)
			return json.Marshal(DevelopResult{Conflict: true, ConflictReason: c.Conflict.Reason})
		}
		msg := fmt.Sprintf("aegis: run %s attempt %d", in.Run.RunID, in.Attempt)
		var sha string
		if out.EditedInPlace {
			sha, err = a.WS.CommitWorking(ctx, wt, msg, key)
			if err != nil {
				return nil, err
			}
		} else {
			// Stub patches are full proposals against the pinned base.
			sha, err = a.WS.ApplyOnto(ctx, wt, in.Workspace.BaseSHA, out.Patch, msg, key)
			if err != nil {
				return nil, temporal.NewNonRetryableApplicationError("patch did not apply: "+err.Error(), ErrScenario, err)
			}
		}
		changes, err := a.WS.Diff(ctx, in.Workspace.worktree(), in.Workspace.BaseSHA, sha)
		if err != nil {
			return nil, err
		}
		files := make([]string, 0, len(changes))
		for _, c := range changes {
			files = append(files, c.Path)
		}
		decisions := make([]map[string]string, 0, len(out.Notes))
		for _, n := range out.Notes {
			decisions = append(decisions, map[string]string{"decision": n.Decision, "reason": n.Reason})
		}
		result, _ := json.Marshal(map[string]any{"status": "IMPLEMENTED", "modified_files": files,
			"commit_sha": sha, "decision_log": decisions})
		if err := contracts.Validate("developer_result", result); err != nil {
			return nil, contractError("developer_result", err)
		}
		if err := writeJSON(filepath.Join(a.runDir(in.Run.RunID), fmt.Sprintf("attempt-%d", in.Attempt), "developer_result.json"), json.RawMessage(result)); err != nil {
			return nil, err
		}
		return json.Marshal(DevelopResult{CommitSHA: sha})
	})
	if err != nil {
		return DevelopResult{}, err
	}
	var res DevelopResult
	return res, json.Unmarshal(raw, &res)
}

// baseTree exports the base commit once into a shared, read-only directory.
func (a *Activities) baseTree(ctx context.Context, wt workspace.Worktree) (string, error) {
	root := filepath.Join(a.Cfg.Path(a.Cfg.Workspace.ArtifactsDir), "base")
	dir := filepath.Join(root, wt.BaseSHA)
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return "", err
	}
	lock, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err == nil {
		return dir, nil
	}
	tmp := fmt.Sprintf("%s.tmp-%d", dir, time.Now().UnixNano())
	if err := a.WS.ExportTree(ctx, wt, wt.BaseSHA, tmp); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".complete"), nil, 0o640); err != nil {
		return "", err
	}
	_ = os.RemoveAll(dir)
	return dir, os.Rename(tmp, dir)
}

type planScope struct {
	Scope struct {
		Planned  []string `json:"planned"`
		Adjacent []string `json:"adjacent"`
	} `json:"scope"`
	DiffBudget struct {
		Files int `json:"files"`
		Lines int `json:"lines"`
	} `json:"diff_budget"`
	InterfaceChanges []string `json:"interface_changes"`
}

// blockStatus maps a hard-blocking gate to the terminal state it causes.
func blockStatus(gate string) Status {
	switch gate {
	case "scope":
		return BlockedScope
	case "openapi", "hidden_tests":
		return EscalatedHuman
	default:
		return BlockedSecurityVeto
	}
}

func (a *Activities) Evaluate(ctx context.Context, in EvalInput) (EvalSummary, error) {
	defer keepAlive(ctx)()
	wt := in.Workspace.worktree()
	head, err := a.WS.Head(ctx, wt)
	if err != nil {
		return EvalSummary{}, err
	}
	if head != in.HeadSHA {
		return EvalSummary{}, fmt.Errorf("worktree is at %s, expected %s", head, in.HeadSHA)
	}
	base, err := a.baseTree(ctx, wt)
	if err != nil {
		return EvalSummary{}, err
	}
	changes, err := a.WS.Diff(ctx, wt, wt.BaseSHA, in.HeadSHA)
	if err != nil {
		return EvalSummary{}, err
	}
	var plan planScope
	if err := json.Unmarshal(in.Plan, &plan); err != nil {
		return EvalSummary{}, err
	}
	repo, _ := a.Cfg.Repo(in.Run.Repo)
	// The plan may tighten the configured diff budget, never loosen it.
	files, lines := a.Cfg.Scope.DiffBudget.Files, a.Cfg.Scope.DiffBudget.Lines
	if plan.DiffBudget.Files > 0 && plan.DiffBudget.Files < files {
		files = plan.DiffBudget.Files
	}
	if plan.DiffBudget.Lines > 0 && plan.DiffBudget.Lines < lines {
		lines = plan.DiffBudget.Lines
	}
	outRoot := filepath.Join(a.runDir(in.Run.RunID), fmt.Sprintf("attempt-%d", in.Attempt), strings.ToLower(in.Tier))
	// Every execution (including Temporal retries) gets a fresh scratch directory.
	// Paths are never deleted and recreated: Docker Desktop's file sharing can keep
	// seeing a deleted path, and earlier executions' reports are kept as evidence.
	execDir := filepath.Join(outRoot, fmt.Sprintf("exec-%d-%d", activity.GetInfo(ctx).Attempt, time.Now().UnixNano()))
	env := gates.Env{
		Sandbox: a.Sandbox, Image: a.Image,
		Timeout:   time.Duration(a.Cfg.Sandbox.TimeoutSeconds) * time.Second,
		MaxOutput: a.Cfg.Sandbox.MaxOutputBytes,
		Repo:      repo, HeadDir: wt.Path, BaseDir: base, BaseSHA: wt.BaseSHA, HeadSHA: in.HeadSHA,
		OutRoot:      execDir,
		BaseCacheDir: filepath.Join(a.Cfg.Path(a.Cfg.Workspace.ArtifactsDir), "base-results", wt.BaseSHA),
		RulesDir:     a.Cfg.Path(a.Cfg.Gates.SemgrepRulesDir),
		HiddenDir:    a.Cfg.Path(a.Cfg.Evals.HiddenTestsDir),
		ReplayDir:    a.Cfg.Path(a.Cfg.Evals.ReplayDir),
		Changes:      changes,
		Planned:      plan.Scope.Planned, Adjacent: plan.Scope.Adjacent,
		Protected:   a.Cfg.Scope.ProtectedPaths,
		BudgetFiles: files, BudgetLines: lines,
		InterfaceChangesDeclared: len(plan.InterfaceChanges) > 0,
	}
	outcome, err := gaterun.Run(ctx, env, in.Gates, a.Cfg.Gates.Parallelism)
	if err != nil {
		return EvalSummary{}, err // infrastructure failure: retried, never a pass
	}
	if err := a.Store.RecordFindings(ctx, in.Run.TenantID, in.Run.RunID, in.Attempt, in.HeadSHA, outcome.Results); err != nil {
		return EvalSummary{}, err
	}
	ref := filepath.Join(outRoot, "outcome.json")
	if err := writeJSON(ref, outcome); err != nil {
		return EvalSummary{}, err
	}
	sum := EvalSummary{Verdict: string(outcome.Verdict), ArtifactRef: ref}
	var diag []string
	for _, r := range outcome.Results {
		sum.Gates = append(sum.Gates, GateBrief{Gate: r.Gate, Verdict: string(r.Verdict), Summary: r.Summary})
		for _, f := range r.Findings {
			if f.PreExisting || f.Verdict == gates.Pass || f.Verdict == gates.Flag {
				continue
			}
			if f.Verdict == gates.HardBlock && sum.BlockStatus == "" {
				sum.BlockStatus = string(blockStatus(r.Gate))
				sum.Reason = fmt.Sprintf("%s: %s %s", r.Gate, f.RuleID, f.Message)
			}
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			diag = append(diag, fmt.Sprintf("[%s] %s %s %s", r.Gate, f.RuleID, loc, f.Message))
		}
	}
	sort.Strings(diag)
	sum.Diagnostics = strings.Join(diag, "\n")
	if len(sum.Diagnostics) > 2000 {
		sum.Diagnostics = sum.Diagnostics[:2000] + "\n…"
	}
	return sum, nil
}

func (a *Activities) Release(ctx context.Context, in ReleaseInput) (ReleaseResult, error) {
	defer keepAlive(ctx)()
	wt := in.Workspace.worktree()
	diff, err := a.WS.DiffText(ctx, wt, wt.BaseSHA, in.HeadSHA)
	if err != nil {
		return ReleaseResult{}, err
	}
	// Evidence hash binds the exact diff and every gate outcome of the final attempt.
	attemptDir := filepath.Join(a.runDir(in.Run.RunID), fmt.Sprintf("attempt-%d", in.Attempts))
	var parts []string
	_ = filepath.WalkDir(attemptDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(p) == "outcome.json" {
			data, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(attemptDir, p)
			parts = append(parts, rel+":"+sha256Hex(data))
		}
		return nil
	})
	sort.Strings(parts)
	evidence := sha256Hex([]byte(strings.Join(append([]string{"base:" + wt.BaseSHA, "head:" + in.HeadSHA,
		"diff:" + sha256Hex([]byte(diff)), "config:" + in.Run.ConfigHash}, parts...), "\n")))

	var spec struct {
		Summary            string   `json:"clarified_summary"`
		Risks              []string `json:"risk_flags"`
		AcceptanceCriteria []struct {
			Must bool `json:"must"`
		} `json:"acceptance_criteria"`
	}
	_ = json.Unmarshal(in.Spec, &spec)
	must := 0
	for _, ac := range spec.AcceptanceCriteria {
		if ac.Must {
			must++
		}
	}
	title := spec.Summary
	if len(title) > 110 {
		title = title[:107] + "..."
	}
	summary, risks := spec.Summary, spec.Risks
	if sz, ok := a.Agents.(agents.Summarizer); ok && a.Cfg.Agents.Mode == "llm" {
		var gateLines []string
		_ = filepath.WalkDir(attemptDir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && filepath.Base(p) == "outcome.json" {
				var o gaterun.Outcome
				if data, err := os.ReadFile(p); err == nil && json.Unmarshal(data, &o) == nil {
					for _, r := range o.Results {
						gateLines = append(gateLines, fmt.Sprintf("%s: %s (%s)", r.Gate, r.Verdict, r.Summary))
					}
				}
			}
			return nil
		})
		sort.Strings(gateLines)
		changes, _ := a.WS.Diff(ctx, wt, wt.BaseSHA, in.HeadSHA)
		var stat []string
		for _, c := range changes {
			stat = append(stat, fmt.Sprintf("%s %s +%d -%d", c.Status, c.Path, c.Added, c.Deleted))
		}
		s, r, err := sz.Summarize(ctx, agents.SummaryInput{Run: a.agentRun(in.Run, in.Attempts),
			RequirementSpec: in.Spec, Plan: in.Plan, GateSummaries: gateLines, DiffStat: strings.Join(stat, "\n")})
		if err != nil {
			return ReleaseResult{}, agentError("release", err)
		}
		summary, risks = s, append(append([]string{}, spec.Risks...), r...)
	}
	packet := map[string]any{
		"pr_title": title, "summary": summary, "base_sha": wt.BaseSHA, "head_sha": in.HeadSHA,
		"evidence_hash": evidence, "risks": risks,
		"rollout": map[string]string{"flag": "aegis_" + strings.ReplaceAll(in.Run.RunID, "-", "_"),
			"canary": "enable for one tenant first", "rollback": "disable the flag; revert the branch"},
		"scorecard": map[string]int{"must_passed": must, "must_total": must, "should_passed": 0, "should_total": 0},
		"cost_usd":  0, "latency_ms": 0, "approval": "PENDING_HUMAN",
	}
	raw, _ := json.Marshal(packet)
	if err := contracts.Validate("release_packet", raw); err != nil {
		return ReleaseResult{}, contractError("release_packet", err)
	}
	ref := filepath.Join(a.runDir(in.Run.RunID), "release_packet.json")
	if err := writeJSON(ref, json.RawMessage(raw)); err != nil {
		return ReleaseResult{}, err
	}
	if err := os.WriteFile(filepath.Join(a.runDir(in.Run.RunID), "final.diff"), []byte(diff), 0o640); err != nil {
		return ReleaseResult{}, err
	}
	return ReleaseResult{EvidenceHash: evidence, PacketRef: ref}, nil
}

func (a *Activities) RecordDecision(ctx context.Context, in DecisionInput) error {
	defer keepAlive(ctx)()
	if in.Decision.Reviewer == "" {
		return temporal.NewNonRetryableApplicationError("decision without a reviewer", ErrScenario, nil)
	}
	d := "REJECT"
	if in.Decision.Approve {
		d = "APPROVE"
	}
	return a.Store.RecordApproval(ctx, in.Run.TenantID, in.Run.RunID, in.Decision.Reviewer, d, in.HeadSHA, in.EvidenceHash, in.Decision.Note)
}

func (a *Activities) Cleanup(ctx context.Context, ws WorkspaceInfo) error {
	defer keepAlive(ctx)()
	err := a.WS.RemoveWorktree(ctx, ws.worktree())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
