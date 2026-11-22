// Package workflow is the AegisSDLC blueprint (PLAN.md §5.2) as a Temporal workflow.
//
// Routing is plain Go: no model ever decides the next step. All I/O lives in
// activities; the workflow only sees small summaries and hashes, so history stays
// small and replays stay deterministic.
package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// Status is a run state. Terminal states end the workflow.
type Status string

const (
	Queued               Status = "QUEUED"
	Preparing            Status = "PREPARING"
	Analyzing            Status = "ANALYZING"
	Architecting         Status = "ARCHITECTING"
	Developing           Status = "DEVELOPING"
	GatesTier1           Status = "GATES_TIER1"
	GatesSecurity        Status = "GATES_SECURITY"
	GatesTier2           Status = "GATES_TIER2"
	Packaging            Status = "PACKAGING"
	AwaitingHuman        Status = "AWAITING_HUMAN"
	Approved             Status = "APPROVED"
	Rejected             Status = "REJECTED"
	Expired              Status = "APPROVAL_EXPIRED"
	HaltedRetryExhausted Status = "HALTED_RETRY_EXHAUSTED"
	BlockedScope         Status = "BLOCKED_SCOPE"
	BlockedSecurityVeto  Status = "BLOCKED_SECURITY_VETO"
	EscalatedHuman       Status = "ESCALATED_HUMAN"
	FailedSchema         Status = "FAILED_SCHEMA"
	Killed               Status = "KILLED"
	BudgetExceeded       Status = "BUDGET_EXCEEDED"
	AgentRefused         Status = "AGENT_REFUSED"
	ProviderAccessDenied Status = "PROVIDER_ACCESS_DENIED"

	// Phase 3 waiting states (not terminal).
	WaitingScopeLease    Status = "WAITING_SCOPE_LEASE"
	QueuedReviewCapacity Status = "QUEUED_REVIEW_CAPACITY"
	PausedProvider       Status = "PAUSED_PROVIDER"
	Rebasing             Status = "REBASING"
	Arbitrating          Status = "ARBITRATING"
)

// Settings is the config snapshot a run executes under. Passed in at start, so a
// config change mid-run can never change a run's decisions.
type Settings struct {
	MaxCIRounds     int           `json:"max_ci_rounds"`
	Tier1           []string      `json:"tier1"`
	Security        []string      `json:"security"`
	Tier2           []string      `json:"tier2"`
	ApprovalExpiry  time.Duration `json:"approval_expiry"`
	ActivityTimeout time.Duration `json:"activity_timeout"`
	// HeartbeatTimeout lets Temporal notice a crashed worker quickly and retry
	// the activity elsewhere, instead of waiting for ActivityTimeout.
	HeartbeatTimeout time.Duration `json:"heartbeat_timeout"`

	// Phase 3 (PLAN.md §10.6).
	MaxArbitrationRounds int            `json:"max_arbitration_rounds"`
	RequiredApprovers    map[string]int `json:"required_approvers"`
	FreezeWindows        []FreezeWindow `json:"freeze_windows,omitempty"`
	LeaseTTL             time.Duration  `json:"lease_ttl"`
	LeaseWait            time.Duration  `json:"lease_wait"`
	LeasePoll            time.Duration  `json:"lease_poll"`
	ReviewWIPCap         int            `json:"review_wip_cap"`
	ReviewPoll           time.Duration  `json:"review_poll"`
	PauseRetry           time.Duration  `json:"pause_retry"`
	MaxRebases           int            `json:"max_rebases"`
	// UsePolicy routes each decision through the Rego approval policy (Phase 5).
	UsePolicy bool `json:"use_policy,omitempty"`
	// MergeQueue sends approved runs to the repo's merge queue (Phase 5).
	MergeQueue bool `json:"merge_queue,omitempty"`
}

// FreezeWindow is a change freeze: approvals inside it are refused.
type FreezeWindow struct {
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Reason string    `json:"reason"`
}

// Input starts a run.
type Input struct {
	RunID       string `json:"run_id"`
	TenantID    string `json:"tenant_id"`
	Repo        string `json:"repo"`
	BaseRef     string `json:"base_ref"`
	Requirement string `json:"requirement"`
	Scenario    string `json:"scenario"`
	ConfigHash  string `json:"config_hash"`
	// Submitter is who asked for the change; maker-checker forbids them approving it.
	Submitter string   `json:"submitter,omitempty"`
	Settings  Settings `json:"settings"`
}

// Decision is the human approval signal.
type Decision struct {
	Approve  bool   `json:"approve"`
	Reviewer string `json:"reviewer"`
	Note     string `json:"note"`
	// Passkey is true when the API verified a WebAuthn assertion bound to this
	// change (run, base, head, evidence); Credential names the passkey used.
	Passkey    bool   `json:"passkey,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// Outcome is the workflow result.
type Outcome struct {
	Status       Status `json:"status"`
	Attempts     int    `json:"attempts"`
	BaseSHA      string `json:"base_sha"`
	HeadSHA      string `json:"head_sha"`
	Branch       string `json:"branch"`
	EvidenceHash string `json:"evidence_hash"`
	Reason       string `json:"reason"`
}

// State is what the "state" query returns.
type State struct {
	Status   Status `json:"status"`
	Attempt  int    `json:"attempt"`
	HeadSHA  string `json:"head_sha"`
	Evidence string `json:"evidence_hash"`
}

const (
	SignalDecision = "decision"
	// SignalResume resumes a run paused because its model providers were down (F5).
	SignalResume = "resume"
	QueryState   = "state"
	// ErrContract marks agent output that failed its JSON Schema.
	ErrContract = "ContractError"
	// ErrScenario marks a stub/scenario misconfiguration.
	ErrScenario = "ScenarioError"
)

// Run is the workflow function.
func Run(ctx workflow.Context, in Input) (Outcome, error) {
	var a *Activities
	state := State{Status: Queued}
	if err := workflow.SetQueryHandler(ctx, QueryState, func() (State, error) { return state, nil }); err != nil {
		return Outcome{}, err
	}
	// Phase 3 behaviour (leases, arbitration, pause/resume, admission control,
	// maker-checker, rebase on base move). Versioned: older runs replay unchanged.
	v3 := workflow.GetVersion(ctx, "phase3", workflow.DefaultVersion, 1) == 1
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: in.Settings.ActivityTimeout,
		HeartbeatTimeout:    in.Settings.HeartbeatTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        time.Second,
			BackoffCoefficient:     2,
			MaximumInterval:        30 * time.Second,
			MaximumAttempts:        5,
			NonRetryableErrorTypes: []string{ErrContract, ErrScenario, ErrAgentRefused, ErrBudget, ErrKilled, ErrSensitiveData, ErrNeedsHuman, ErrProviderDenied},
		},
	})

	out := Outcome{}
	seq := 0
	record := func(c workflow.Context, node string, status Status, attempt int, commit string, detail map[string]any) error {
		seq++
		state.Status, state.Attempt = status, attempt
		if commit != "" {
			state.HeadSHA = commit
		}
		return workflow.ExecuteActivity(c, a.RecordTransition, TransitionInput{
			Run: in, Seq: seq, Node: node, Status: status, Attempt: attempt, CommitSHA: commit,
			BaseSHA: out.BaseSHA, Branch: out.Branch, Detail: detail}).Get(c, nil)
	}
	var ws WorkspaceInfo
	cleanup := func() {
		if ws.Path == "" {
			return
		}
		dctx, _ := workflow.NewDisconnectedContext(actx)
		_ = workflow.ExecuteActivity(dctx, a.Cleanup, ws).Get(dctx, nil)
	}
	finish := func(status Status, reason string) (Outcome, error) {
		out.Status, out.Reason = status, reason
		dctx, _ := workflow.NewDisconnectedContext(actx)
		if err := record(dctx, "finish", status, out.Attempts, out.HeadSHA, map[string]any{"reason": reason}); err != nil {
			return out, err
		}
		if v3 {
			_ = workflow.ExecuteActivity(dctx, a.ReleaseLeases, in).Get(dctx, nil)
		}
		cleanup()
		return out, nil
	}
	// terminal maps a typed agent failure to the state the run ends in.
	terminal := func(err error) (Status, bool) {
		var appErr *temporal.ApplicationError
		if !errors.As(err, &appErr) {
			return "", false
		}
		switch appErr.Type() {
		case ErrContract:
			return FailedSchema, true
		case ErrAgentRefused:
			return AgentRefused, true
		case ErrBudget:
			return BudgetExceeded, true
		case ErrKilled:
			return Killed, true
		case ErrSensitiveData:
			return BlockedSecurityVeto, true
		case ErrNeedsHuman:
			return EscalatedHuman, true
		case ErrProviderDenied:
			return ProviderAccessDenied, true
		}
		return "", false
	}
	contractFailure := func(err error) bool { _, ok := terminal(err); return ok }
	reasonOf := func(err error) string {
		var appErr *temporal.ApplicationError
		if errors.As(err, &appErr) {
			return appErr.Message()
		}
		return err.Error()
	}
	canceled := func(err error) bool { return temporal.IsCanceledError(err) || ctx.Err() != nil }

	if err := workflow.ExecuteActivity(actx, a.CreateRun, in).Get(actx, nil); err != nil {
		if st, ok := terminal(err); ok {
			out.Status, out.Reason = st, reasonOf(err)
			return out, nil // refused before the run existed; nothing to record or clean up
		}
		return out, err
	}

	if err := record(actx, "prepare", Preparing, 0, "", nil); err != nil {
		return out, err
	}
	if err := workflow.ExecuteActivity(actx, a.Prepare, in).Get(actx, &ws); err != nil {
		if canceled(err) {
			return finish(Killed, "run was stopped")
		}
		return out, err
	}
	out.BaseSHA, out.Branch = ws.BaseSHA, ws.Branch

	if err := record(actx, "analyze", Analyzing, 0, "", nil); err != nil {
		return out, err
	}
	// providerDown reports an agent step that failed because every model provider
	// stayed unavailable through Temporal's retries.
	providerDown := func(err error) bool {
		var appErr *temporal.ApplicationError
		return errors.As(err, &appErr) && appErr.Type() == ErrProviderUnavailable
	}
	// withPause runs an agent step; if the providers are down it pauses the run
	// (PAUSED_PROVIDER) until a resume signal or the pause interval, then retries
	// the same step (F5). Without Phase 3 it just runs the step.
	withPause := func(node string, resume Status, attempt int, step func() error) error {
		for {
			err := step()
			if err == nil || !v3 || !providerDown(err) || canceled(err) {
				return err
			}
			if rerr := record(actx, "pause", PausedProvider, attempt, "", map[string]any{"step": node, "reason": reasonOf(err)}); rerr != nil {
				return rerr
			}
			sel := workflow.NewSelector(ctx)
			sel.AddReceive(workflow.GetSignalChannel(ctx, SignalResume), func(c workflow.ReceiveChannel, _ bool) {
				var v any
				c.Receive(ctx, &v)
			})
			tctx, cancelT := workflow.WithCancel(ctx)
			sel.AddFuture(workflow.NewTimer(tctx, in.Settings.PauseRetry), func(workflow.Future) {})
			sel.Select(ctx)
			cancelT()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if rerr := record(actx, node, resume, attempt, "", map[string]any{"resumed": true}); rerr != nil {
				return rerr
			}
		}
	}
	// agentFail maps an agent step error to the run's end.
	agentFail := func(err error) (Outcome, error) {
		if contractFailure(err) {
			st, _ := terminal(err)
			return finish(st, reasonOf(err))
		}
		if canceled(err) {
			return finish(Killed, "run was stopped")
		}
		return out, err
	}

	var spec AgentOutput
	if err := withPause("analyze", Analyzing, 0, func() error {
		return workflow.ExecuteActivity(actx, a.Analyze, AgentInput{Run: in, Workspace: ws}).Get(actx, &spec)
	}); err != nil {
		return agentFail(err)
	}

	if err := record(actx, "architect", Architecting, 0, "", nil); err != nil {
		return out, err
	}
	var plan AgentOutput
	architect := func(conflict string, round int) error {
		return withPause("architect", Architecting, 0, func() error {
			return workflow.ExecuteActivity(actx, a.Architect, AgentInput{Run: in, Workspace: ws, Prior: spec.JSON,
				Conflict: conflict, Round: round}).Get(actx, &plan)
		})
	}
	if err := architect("", 0); err != nil {
		return agentFail(err)
	}

	// stop carries an early return out of a helper.
	type stop struct {
		out Outcome
		err error
	}
	halt := func(o Outcome, e error) *stop { return &stop{o, e} }

	// P4: scope leases on the planned paths. Waits while another run holds an
	// overlapping path; escalates after LeaseWait.
	takeLeases := func(attempt int) *stop {
		if !v3 {
			return nil
		}
		var ps planScope
		_ = json.Unmarshal(plan.JSON, &ps)
		if len(ps.Scope.Planned) == 0 {
			return nil
		}
		var waited time.Duration
		waiting := false
		for {
			var lr LeaseResult
			if err := workflow.ExecuteActivity(actx, a.AcquireLeases, LeaseInput{Run: in, Paths: ps.Scope.Planned}).Get(actx, &lr); err != nil {
				if canceled(err) {
					return halt(finish(Killed, "run was stopped"))
				}
				return halt(out, err)
			}
			if lr.Granted {
				return nil
			}
			if !waiting {
				waiting = true
				if err := record(actx, "lease_wait", WaitingScopeLease, attempt, "", map[string]any{"holder_run": lr.HolderRun, "path": lr.Path}); err != nil {
					return halt(out, err)
				}
			}
			if waited >= in.Settings.LeaseWait {
				return halt(finish(EscalatedHuman, fmt.Sprintf("planned path %s is held by run %s (scope lease)", lr.Path, lr.HolderRun)))
			}
			if err := workflow.Sleep(ctx, in.Settings.LeasePoll); err != nil {
				return halt(finish(Killed, "run was stopped while waiting for a scope lease"))
			}
			waited += in.Settings.LeasePoll
		}
	}
	if s := takeLeases(0); s != nil {
		return s.out, s.err
	}

	// runGates evaluates one commit through every tier.
	runGates := func(attempt int, head string) (retry bool, diag string, s *stop) {
		tiers := []struct {
			status Status
			gates  []string
		}{{GatesTier1, in.Settings.Tier1}, {GatesSecurity, in.Settings.Security}, {GatesTier2, in.Settings.Tier2}}
		// PLAN.md §10.5 L3: all tiers start together on the same commit; the verdict
		// is read in tier order, so the result is exactly what sequential tiers would
		// give, but the developer also gets every failure at once.
		if workflow.GetVersion(ctx, "parallel-gate-tiers", workflow.DefaultVersion, 1) == 1 {
			var active []struct {
				status Status
				gates  []string
			}
			for _, tier := range tiers {
				if len(tier.gates) > 0 {
					active = append(active, tier)
				}
			}
			for _, tier := range active {
				if err := record(actx, "gates", tier.status, attempt, head, nil); err != nil {
					return false, "", halt(out, err)
				}
			}
			futures := make([]workflow.Future, len(active))
			for i, tier := range active {
				futures[i] = workflow.ExecuteActivity(actx, a.Evaluate, EvalInput{Run: in, Workspace: ws, Plan: plan.JSON,
					HeadSHA: head, Attempt: attempt, Tier: string(tier.status), Gates: tier.gates})
			}
			var diags []string
			for i := range active {
				var ev EvalSummary
				if err := futures[i].Get(actx, &ev); err != nil {
					if canceled(err) {
						return false, "", halt(finish(Killed, "run was stopped"))
					}
					return false, "", halt(out, err)
				}
				switch ev.Verdict {
				case "HARD_BLOCK":
					return false, "", halt(finish(Status(ev.BlockStatus), ev.Reason))
				case "REMEDIABLE_FAIL":
					retry = true
					if ev.Diagnostics != "" {
						diags = append(diags, ev.Diagnostics)
					}
				}
			}
			return retry, strings.Join(diags, "\n"), nil
		}
		for _, tier := range tiers {
			if len(tier.gates) == 0 {
				continue
			}
			if err := record(actx, "gates", tier.status, attempt, head, nil); err != nil {
				return false, "", halt(out, err)
			}
			var ev EvalSummary
			err := workflow.ExecuteActivity(actx, a.Evaluate, EvalInput{Run: in, Workspace: ws, Plan: plan.JSON,
				HeadSHA: head, Attempt: attempt, Tier: string(tier.status), Gates: tier.gates}).Get(actx, &ev)
			if err != nil {
				if canceled(err) {
					return false, "", halt(finish(Killed, "run was stopped"))
				}
				return false, "", halt(out, err)
			}
			switch ev.Verdict {
			case "HARD_BLOCK":
				return false, "", halt(finish(Status(ev.BlockStatus), ev.Reason))
			case "REMEDIABLE_FAIL":
				return true, ev.Diagnostics, nil
			}
		}
		return false, "", nil
	}

	diagnostics := ""
	passed := false
	arbitration := 0
	for attempt := 1; attempt <= in.Settings.MaxCIRounds; attempt++ {
		if attempt > 1 {
			if s := takeLeases(attempt); s != nil { // renew
				return s.out, s.err
			}
		}
		out.Attempts = attempt
		if err := record(actx, "develop", Developing, attempt, "", nil); err != nil {
			return out, err
		}
		var dev DevelopResult
		if err := withPause("develop", Developing, attempt, func() error {
			return workflow.ExecuteActivity(actx, a.Develop, DevelopActivityInput{Run: in, Workspace: ws, Spec: spec.JSON, Plan: plan.JSON,
				Attempt: attempt, Diagnostics: diagnostics, Round: arbitration}).Get(actx, &dev)
		}); err != nil {
			return agentFail(err)
		}
		if dev.Conflict {
			// F1: send the conflict back to the Architect for a revised plan.
			if v3 && arbitration < in.Settings.MaxArbitrationRounds {
				arbitration++
				if err := record(actx, "arbitrate", Arbitrating, attempt, "", map[string]any{"conflict": dev.ConflictReason, "round": arbitration}); err != nil {
					return out, err
				}
				if err := architect(dev.ConflictReason, arbitration); err != nil {
					return agentFail(err)
				}
				if s := takeLeases(attempt); s != nil {
					return s.out, s.err
				}
				diagnostics = ""
				attempt-- // the revised plan gets a full set of attempts' worth of this round
				continue
			}
			return finish(EscalatedHuman, "developer raised a design conflict: "+dev.ConflictReason)
		}
		out.HeadSHA = dev.CommitSHA
		retry, diag, s := runGates(attempt, dev.CommitSHA)
		if s != nil {
			return s.out, s.err
		}
		if !retry {
			passed = true
			break
		}
		diagnostics = diag
	}
	if !passed {
		return finish(HaltedRetryExhausted, fmt.Sprintf("checks still failing after %d attempt(s): %s", out.Attempts, diagnostics))
	}

	rebases := 0
	for {
		if err := record(actx, "release", Packaging, out.Attempts, out.HeadSHA, nil); err != nil {
			return out, err
		}
		var pkt ReleaseResult
		if err := withPause("release", Packaging, out.Attempts, func() error {
			return workflow.ExecuteActivity(actx, a.Release, ReleaseInput{Run: in, Workspace: ws, Spec: spec.JSON,
				Plan: plan.JSON, HeadSHA: out.HeadSHA, Attempts: out.Attempts}).Get(actx, &pkt)
		}); err != nil {
			return agentFail(err)
		}
		out.EvidenceHash, state.Evidence = pkt.EvidenceHash, pkt.EvidenceHash

		// P5: don't flood reviewers; park until the tenant's review queue has room.
		if v3 && in.Settings.ReviewWIPCap > 0 {
			parked := false
			for {
				var n int
				if err := workflow.ExecuteActivity(actx, a.ReviewLoad, in).Get(actx, &n); err != nil {
					if canceled(err) {
						return finish(Killed, "run was stopped")
					}
					return out, err
				}
				if n < in.Settings.ReviewWIPCap {
					break
				}
				if !parked {
					parked = true
					if err := record(actx, "review_queue", QueuedReviewCapacity, out.Attempts, out.HeadSHA, map[string]any{"awaiting": n, "cap": in.Settings.ReviewWIPCap}); err != nil {
						return out, err
					}
				}
				if err := workflow.Sleep(ctx, in.Settings.ReviewPoll); err != nil {
					return finish(Killed, "run was stopped while queued for review")
				}
			}
		}

		if err := record(actx, "await_human", AwaitingHuman, out.Attempts, out.HeadSHA, map[string]any{"evidence_hash": pkt.EvidenceHash, "risk_tier": pkt.RiskTier}); err != nil {
			return out, err
		}
		// P3: maker-checker, risk-tiered approver count, freeze windows.
		need := 1
		if v3 {
			if n := in.Settings.RequiredApprovers[pkt.RiskTier]; n > 1 {
				need = n
			}
		}
		approvers := map[string]bool{}
		timerCtx, cancelTimer := workflow.WithCancel(ctx)
		timer := workflow.NewTimer(timerCtx, in.Settings.ApprovalExpiry)
		decisions := workflow.GetSignalChannel(ctx, SignalDecision)
		for len(approvers) < need {
			var decision Decision
			got, expired := false, false
			sel := workflow.NewSelector(ctx)
			sel.AddReceive(decisions, func(c workflow.ReceiveChannel, _ bool) {
				c.Receive(ctx, &decision)
				got = true
			})
			sel.AddFuture(timer, func(workflow.Future) { expired = true })
			sel.Select(ctx)
			if ctx.Err() != nil {
				cancelTimer()
				return finish(Killed, "run was stopped while awaiting approval")
			}
			if expired && !got {
				return finish(Expired, "no human decision before the approval expired")
			}
			refused := ""
			policyDone := false
			// Phase 5: the Rego approval policy decides (when configured).
			if v3 && in.Settings.UsePolicy && workflow.GetVersion(ctx, "approval-policy", workflow.DefaultVersion, 1) == 1 {
				names := make([]string, 0, len(approvers))
				for r := range approvers {
					names = append(names, r)
				}
				sort.Strings(names)
				var pr PolicyResult
				if err := workflow.ExecuteActivity(actx, a.DecidePolicy, PolicyInput{Run: in, Decision: decision, RiskTier: pkt.RiskTier,
					Approvals: names, Now: workflow.Now(ctx)}).Get(actx, &pr); err != nil {
					cancelTimer()
					return out, err
				}
				if !pr.Accept {
					refused = pr.Reason
					if refused == "" {
						refused = "refused by approval policy"
					}
				}
				if pr.Needed > need {
					need = pr.Needed
				}
				policyDone = true
			}
			if v3 && !policyDone {
				switch {
				case decision.Approve && in.Submitter != "" && decision.Reviewer == in.Submitter:
					refused = "maker-checker: the submitter cannot approve their own change"
				case decision.Approve && approvers[decision.Reviewer]:
					refused = "duplicate approval from the same reviewer"
				case decision.Approve:
					if w, ok := inFreeze(in.Settings.FreezeWindows, workflow.Now(ctx)); ok {
						refused = "change freeze in force: " + w.Reason
					}
				}
			}
			if err := workflow.ExecuteActivity(actx, a.RecordDecision, DecisionInput{Run: in, Decision: decision,
				HeadSHA: out.HeadSHA, EvidenceHash: out.EvidenceHash, Refused: refused}).Get(actx, nil); err != nil {
				cancelTimer()
				return out, err
			}
			if refused != "" {
				continue
			}
			if !decision.Approve {
				cancelTimer()
				return finish(Rejected, "rejected by "+decision.Reviewer)
			}
			approvers[decision.Reviewer] = true
		}
		cancelTimer()

		// P9: evidence is bound to (base, head). If the base moved, rebase, re-run
		// every gate on the new head, and ask for approval again.
		if v3 && rebases < in.Settings.MaxRebases {
			var br BaseResult
			if err := workflow.ExecuteActivity(actx, a.RebaseIfMoved, BaseInput{Run: in, Workspace: ws, HeadSHA: out.HeadSHA, Round: rebases}).Get(actx, &br); err != nil {
				if canceled(err) {
					return finish(Killed, "run was stopped")
				}
				return out, err
			}
			if br.Moved {
				if br.Conflict {
					return finish(EscalatedHuman, "the base branch moved and the change no longer rebases cleanly; a human must resolve it")
				}
				rebases++
				ws.BaseSHA, out.BaseSHA, out.HeadSHA = br.BaseSHA, br.BaseSHA, br.HeadSHA
				if err := record(actx, "rebase", Rebasing, out.Attempts, out.HeadSHA, map[string]any{"new_base": br.BaseSHA, "round": rebases}); err != nil {
					return out, err
				}
				retry, diag, s := runGates(out.Attempts, out.HeadSHA)
				if s != nil {
					return s.out, s.err
				}
				if retry {
					return finish(EscalatedHuman, "checks fail after rebasing onto the new base: "+diag)
				}
				continue // new evidence; approvals start over
			}
		}
		names := make([]string, 0, len(approvers))
		for r := range approvers {
			names = append(names, r)
		}
		sort.Strings(names)
		reason := "approved by " + strings.Join(names, ", ")
		// Phase 5: land it through the repo's merge queue (rebase on tip, re-test, fast-forward).
		if v3 && in.Settings.MergeQueue && workflow.GetVersion(ctx, "merge-queue", workflow.DefaultVersion, 1) == 1 {
			if err := workflow.ExecuteActivity(actx, a.EnqueueMerge, MergeRequest{Run: in, Workspace: ws, HeadSHA: out.HeadSHA, Plan: plan.JSON}).Get(actx, nil); err != nil {
				return out, err
			}
			reason += "; queued to merge"
		}
		return finish(Approved, reason)
	}
}
