// Package workflow is the AegisSDLC blueprint (PLAN.md §5.2) as a Temporal workflow.
//
// Routing is plain Go: no model ever decides the next step. All I/O lives in
// activities; the workflow only sees small summaries and hashes, so history stays
// small and replays stay deterministic.
package workflow

import (
	"errors"
	"fmt"
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
}

// Input starts a run.
type Input struct {
	RunID       string   `json:"run_id"`
	TenantID    string   `json:"tenant_id"`
	Repo        string   `json:"repo"`
	BaseRef     string   `json:"base_ref"`
	Requirement string   `json:"requirement"`
	Scenario    string   `json:"scenario"`
	ConfigHash  string   `json:"config_hash"`
	Settings    Settings `json:"settings"`
}

// Decision is the human approval signal.
type Decision struct {
	Approve  bool   `json:"approve"`
	Reviewer string `json:"reviewer"`
	Note     string `json:"note"`
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
	QueryState     = "state"
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
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: in.Settings.ActivityTimeout,
		HeartbeatTimeout:    in.Settings.HeartbeatTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:        time.Second,
			BackoffCoefficient:     2,
			MaximumInterval:        30 * time.Second,
			MaximumAttempts:        5,
			NonRetryableErrorTypes: []string{ErrContract, ErrScenario},
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
		cleanup()
		return out, nil
	}
	contractFailure := func(err error) bool {
		var appErr *temporal.ApplicationError
		return errors.As(err, &appErr) && appErr.Type() == ErrContract
	}
	canceled := func(err error) bool { return temporal.IsCanceledError(err) || ctx.Err() != nil }

	if err := workflow.ExecuteActivity(actx, a.CreateRun, in).Get(actx, nil); err != nil {
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
	var spec AgentOutput
	if err := workflow.ExecuteActivity(actx, a.Analyze, AgentInput{Run: in, Workspace: ws}).Get(actx, &spec); err != nil {
		if contractFailure(err) {
			return finish(FailedSchema, "analyst output failed its contract")
		}
		if canceled(err) {
			return finish(Killed, "run was stopped")
		}
		return out, err
	}

	if err := record(actx, "architect", Architecting, 0, "", nil); err != nil {
		return out, err
	}
	var plan AgentOutput
	if err := workflow.ExecuteActivity(actx, a.Architect, AgentInput{Run: in, Workspace: ws, Prior: spec.JSON}).Get(actx, &plan); err != nil {
		if contractFailure(err) {
			return finish(FailedSchema, "architect output failed its contract")
		}
		if canceled(err) {
			return finish(Killed, "run was stopped")
		}
		return out, err
	}

	diagnostics := ""
	passed := false
	for attempt := 1; attempt <= in.Settings.MaxCIRounds; attempt++ {
		out.Attempts = attempt
		if err := record(actx, "develop", Developing, attempt, "", nil); err != nil {
			return out, err
		}
		var dev DevelopResult
		err := workflow.ExecuteActivity(actx, a.Develop, DevelopActivityInput{Run: in, Workspace: ws, Plan: plan.JSON,
			Attempt: attempt, Diagnostics: diagnostics}).Get(actx, &dev)
		if err != nil {
			if contractFailure(err) {
				return finish(FailedSchema, "developer output failed its contract")
			}
			if canceled(err) {
				return finish(Killed, "run was stopped")
			}
			return out, err
		}
		if dev.Conflict {
			return finish(EscalatedHuman, "developer raised a design conflict: "+dev.ConflictReason)
		}
		out.HeadSHA = dev.CommitSHA

		tiers := []struct {
			status Status
			gates  []string
		}{{GatesTier1, in.Settings.Tier1}, {GatesSecurity, in.Settings.Security}, {GatesTier2, in.Settings.Tier2}}
		retry := false
		for _, tier := range tiers {
			if len(tier.gates) == 0 {
				continue
			}
			if err := record(actx, "gates", tier.status, attempt, dev.CommitSHA, nil); err != nil {
				return out, err
			}
			var ev EvalSummary
			err := workflow.ExecuteActivity(actx, a.Evaluate, EvalInput{Run: in, Workspace: ws, Plan: plan.JSON,
				HeadSHA: dev.CommitSHA, Attempt: attempt, Tier: string(tier.status), Gates: tier.gates}).Get(actx, &ev)
			if err != nil {
				if canceled(err) {
					return finish(Killed, "run was stopped")
				}
				return out, err
			}
			switch ev.Verdict {
			case "HARD_BLOCK":
				return finish(Status(ev.BlockStatus), ev.Reason)
			case "REMEDIABLE_FAIL":
				diagnostics = ev.Diagnostics
				retry = true
			}
			if retry {
				break
			}
		}
		if !retry {
			passed = true
			break
		}
	}
	if !passed {
		return finish(HaltedRetryExhausted, fmt.Sprintf("checks still failing after %d attempt(s): %s", out.Attempts, diagnostics))
	}

	if err := record(actx, "release", Packaging, out.Attempts, out.HeadSHA, nil); err != nil {
		return out, err
	}
	var pkt ReleaseResult
	if err := workflow.ExecuteActivity(actx, a.Release, ReleaseInput{Run: in, Workspace: ws, Spec: spec.JSON,
		Plan: plan.JSON, HeadSHA: out.HeadSHA, Attempts: out.Attempts}).Get(actx, &pkt); err != nil {
		if contractFailure(err) {
			return finish(FailedSchema, "release packet failed its contract")
		}
		return out, err
	}
	out.EvidenceHash, state.Evidence = pkt.EvidenceHash, pkt.EvidenceHash

	if err := record(actx, "await_human", AwaitingHuman, out.Attempts, out.HeadSHA, map[string]any{"evidence_hash": pkt.EvidenceHash}); err != nil {
		return out, err
	}
	var decision Decision
	got, expired := false, false
	sel := workflow.NewSelector(ctx)
	sel.AddReceive(workflow.GetSignalChannel(ctx, SignalDecision), func(c workflow.ReceiveChannel, _ bool) {
		c.Receive(ctx, &decision)
		got = true
	})
	timerCtx, cancelTimer := workflow.WithCancel(ctx)
	sel.AddFuture(workflow.NewTimer(timerCtx, in.Settings.ApprovalExpiry), func(workflow.Future) { expired = true })
	sel.Select(ctx)
	cancelTimer()
	if ctx.Err() != nil {
		return finish(Killed, "run was stopped while awaiting approval")
	}
	if expired && !got {
		return finish(Expired, "no human decision before the approval expired")
	}
	if err := workflow.ExecuteActivity(actx, a.RecordDecision, DecisionInput{Run: in, Decision: decision,
		HeadSHA: out.HeadSHA, EvidenceHash: out.EvidenceHash}).Get(actx, nil); err != nil {
		return out, err
	}
	if decision.Approve {
		return finish(Approved, "approved by "+decision.Reviewer)
	}
	return finish(Rejected, "rejected by "+decision.Reviewer)
}
