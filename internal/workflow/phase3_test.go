package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
)

// p3Input is a run with Phase 3 settings and a plan that has planned paths.
func p3Input() Input {
	in := input()
	in.Submitter = "requester:bob"
	in.Settings.MaxArbitrationRounds = 1
	in.Settings.RequiredApprovers = map[string]int{"low": 1, "high": 2}
	in.Settings.LeaseTTL, in.Settings.LeaseWait, in.Settings.LeasePoll = 30*time.Minute, 2*time.Minute, 30*time.Second
	in.Settings.ReviewWIPCap, in.Settings.ReviewPoll = 2, 30*time.Second
	in.Settings.PauseRetry = 5 * time.Minute
	in.Settings.MaxRebases = 3
	return in
}

var planWithScope = json.RawMessage(`{"scope":{"planned":["app/webhooks/handler.py"],"adjacent":["tests/**"]}}`)

func (h *harness) signal(d time.Duration, approve bool, reviewer string) {
	h.env.RegisterDelayedCallback(func() {
		h.env.SignalWorkflow(SignalDecision, Decision{Approve: approve, Reviewer: reviewer})
	}, d)
}

type decisions struct {
	mu   sync.Mutex
	list []DecisionInput
}

func (d *decisions) mock(h *harness) {
	h.env.OnActivity(h.a.RecordDecision, mock.Anything, mock.Anything).Return(func(_ context.Context, in DecisionInput) error {
		d.mu.Lock()
		d.list = append(d.list, in)
		d.mu.Unlock()
		return nil
	})
}

func release(h *harness, tier string) {
	h.env.OnActivity(h.a.Release, mock.Anything, mock.Anything).Return(ReleaseResult{EvidenceHash: "ev", RiskTier: tier}, nil)
}

func TestMakerCheckerRefusesSelfApprovalAndNeedsTwoForHighRisk(t *testing.T) {
	h := newHarness(t, "Release", "RecordDecision")
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.evaluate(nil)
	release(h, "high")
	var d decisions
	d.mock(h)
	h.signal(time.Minute, true, "requester:bob") // the submitter: refused
	h.signal(2*time.Minute, true, "alice")       // 1 of 2
	h.signal(3*time.Minute, true, "alice")       // duplicate: refused
	h.signal(4*time.Minute, true, "carol")       // 2 of 2
	out := h.runWith(t, p3Input())
	if out.Status != Approved || !strings.Contains(out.Reason, "alice, carol") {
		t.Fatalf("outcome %+v", out)
	}
	var refused []string
	for _, x := range d.list {
		if x.Refused != "" {
			refused = append(refused, x.Decision.Reviewer+": "+x.Refused)
		}
	}
	if len(refused) != 2 || !strings.Contains(refused[0], "maker-checker") || !strings.Contains(refused[1], "duplicate") {
		t.Fatalf("refusals %v", refused)
	}
}

func TestLowRiskNeedsOneApproverAndSubmitterMayWithdraw(t *testing.T) {
	h := newHarness(t, "Release")
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.evaluate(nil)
	release(h, "low")
	h.signal(time.Minute, false, "requester:bob") // withdrawing your own change is allowed
	if out := h.runWith(t, p3Input()); out.Status != Rejected {
		t.Fatalf("outcome %+v", out)
	}
}

func TestFreezeWindowRefusesApproval(t *testing.T) {
	h := newHarness(t, "Release", "RecordDecision")
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.evaluate(nil)
	release(h, "low")
	var d decisions
	d.mock(h)
	in := p3Input()
	now := h.env.Now()
	in.Settings.FreezeWindows = []FreezeWindow{{Start: now.Add(-time.Hour), End: now.Add(90 * time.Minute), Reason: "month-end settlement"}}
	h.signal(time.Minute, true, "alice") // inside the freeze: refused
	h.signal(2*time.Hour, true, "alice") // after the freeze: accepted
	in.Settings.ApprovalExpiry = 3 * time.Hour
	out := h.runWith(t, in)
	if out.Status != Approved || len(d.list) != 2 || !strings.Contains(d.list[0].Refused, "month-end") {
		t.Fatalf("outcome %+v decisions %+v", out, d.list)
	}
}

func TestScopeLeaseWaitsThenProceeds(t *testing.T) {
	h := newHarness(t, "Architect", "AcquireLeases", "RecordTransition")
	h.env.OnActivity(h.a.Architect, mock.Anything, mock.Anything).Return(AgentOutput{JSON: planWithScope}, nil)
	calls := 0
	h.env.OnActivity(h.a.AcquireLeases, mock.Anything, mock.Anything).Return(func(_ context.Context, in LeaseInput) (LeaseResult, error) {
		calls++
		if in.Paths[0] != "app/webhooks/handler.py" {
			t.Errorf("leased %v", in.Paths)
		}
		if calls < 3 {
			return LeaseResult{HolderRun: "other-run", Path: "app/webhooks/handler.py"}, nil
		}
		return LeaseResult{Granted: true}, nil
	})
	var statuses []Status
	h.env.OnActivity(h.a.RecordTransition, mock.Anything, mock.Anything).Return(func(_ context.Context, tr TransitionInput) error {
		statuses = append(statuses, tr.Status)
		return nil
	})
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.evaluate(nil)
	h.approveAfter(10*time.Minute, true)
	out := h.runWith(t, p3Input())
	if out.Status != Approved || calls != 3 {
		t.Fatalf("outcome %+v after %d lease calls", out, calls)
	}
	seen := false
	for _, s := range statuses {
		seen = seen || s == WaitingScopeLease
	}
	if !seen {
		t.Fatalf("never recorded %s: %v", WaitingScopeLease, statuses)
	}
}

func TestScopeLeaseEscalatesAfterWait(t *testing.T) {
	h := newHarness(t, "Architect", "AcquireLeases")
	h.env.OnActivity(h.a.Architect, mock.Anything, mock.Anything).Return(AgentOutput{JSON: planWithScope}, nil)
	h.env.OnActivity(h.a.AcquireLeases, mock.Anything, mock.Anything).Return(LeaseResult{HolderRun: "other-run", Path: "app/webhooks/handler.py"}, nil)
	out := h.runWith(t, p3Input())
	if out.Status != EscalatedHuman || !strings.Contains(out.Reason, "other-run") {
		t.Fatalf("outcome %+v", out)
	}
}

func TestArbitrationReplansOnceThenSucceeds(t *testing.T) {
	h := newHarness(t, "Architect")
	var conflicts []string
	h.env.OnActivity(h.a.Architect, mock.Anything, mock.Anything).Return(func(_ context.Context, in AgentInput) (AgentOutput, error) {
		conflicts = append(conflicts, in.Conflict)
		return AgentOutput{JSON: planWithScope}, nil
	})
	h.develop(DevelopResult{Conflict: true, ConflictReason: "needs app/ledger/store.py"}, DevelopResult{CommitSHA: "c2"})
	h.evaluate(nil)
	h.approveAfter(10*time.Minute, true)
	out := h.runWith(t, p3Input())
	if out.Status != Approved || len(conflicts) != 2 || conflicts[1] != "needs app/ledger/store.py" {
		t.Fatalf("outcome %+v conflicts %q", out, conflicts)
	}
}

func TestArbitrationEscalatesWhenConflictPersists(t *testing.T) {
	h := newHarness(t)
	h.develop(DevelopResult{Conflict: true, ConflictReason: "x"})
	h.evaluate(nil)
	if out := h.runWith(t, p3Input()); out.Status != EscalatedHuman {
		t.Fatalf("outcome %+v", out)
	}
}

func TestProviderOutagePausesThenResumes(t *testing.T) {
	h := newHarness(t, "RecordTransition")
	n := 0
	h.env.OnActivity(h.a.Develop, mock.Anything, mock.Anything).Return(func(_ context.Context, in DevelopActivityInput) (DevelopResult, error) {
		n++
		if n == 1 {
			return DevelopResult{}, temporal.NewNonRetryableApplicationError("developer: all model providers unavailable", ErrProviderUnavailable, nil)
		}
		return DevelopResult{CommitSHA: "c1"}, nil
	})
	var statuses []Status
	h.env.OnActivity(h.a.RecordTransition, mock.Anything, mock.Anything).Return(func(_ context.Context, tr TransitionInput) error {
		statuses = append(statuses, tr.Status)
		return nil
	})
	h.evaluate(nil)
	h.env.RegisterDelayedCallback(func() { h.env.SignalWorkflow(SignalResume, nil) }, time.Minute)
	h.approveAfter(10*time.Minute, true)
	out := h.runWith(t, p3Input())
	paused := false
	for _, s := range statuses {
		paused = paused || s == PausedProvider
	}
	if out.Status != Approved || n != 2 || !paused {
		t.Fatalf("outcome %+v develop calls %d statuses %v", out, n, statuses)
	}
}

func TestReviewCapacityParksRun(t *testing.T) {
	h := newHarness(t, "ReviewLoad", "RecordTransition")
	loads := []int{2, 2, 1}
	i := 0
	h.env.OnActivity(h.a.ReviewLoad, mock.Anything, mock.Anything).Return(func(_ context.Context, _ Input) (int, error) {
		v := loads[min(i, len(loads)-1)]
		i++
		return v, nil
	})
	var statuses []Status
	h.env.OnActivity(h.a.RecordTransition, mock.Anything, mock.Anything).Return(func(_ context.Context, tr TransitionInput) error {
		statuses = append(statuses, tr.Status)
		return nil
	})
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.evaluate(nil)
	h.approveAfter(10*time.Minute, true)
	out := h.runWith(t, p3Input())
	queued := false
	for _, s := range statuses {
		queued = queued || s == QueuedReviewCapacity
	}
	if out.Status != Approved || i != 3 || !queued {
		t.Fatalf("outcome %+v load checks %d statuses %v", out, i, statuses)
	}
}

func TestBaseMovedRebasesRetestsAndReapproves(t *testing.T) {
	h := newHarness(t, "RebaseIfMoved", "Release")
	release(h, "low")
	rounds := 0
	h.env.OnActivity(h.a.RebaseIfMoved, mock.Anything, mock.Anything).Return(func(_ context.Context, in BaseInput) (BaseResult, error) {
		rounds++
		if in.Round == 0 {
			return BaseResult{Moved: true, BaseSHA: "base2", HeadSHA: "c1-rebased"}, nil
		}
		return BaseResult{BaseSHA: "base2", HeadSHA: in.HeadSHA}, nil
	})
	var heads []string
	h.env.OnActivity(h.a.Evaluate, mock.Anything, mock.Anything).Return(func(_ context.Context, in EvalInput) (EvalSummary, error) {
		if in.Tier == string(GatesTier1) {
			heads = append(heads, in.HeadSHA+"@"+in.Workspace.BaseSHA)
		}
		return EvalSummary{Verdict: "PASS"}, nil
	})
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.signal(time.Minute, true, "alice")
	h.signal(30*time.Minute, true, "alice") // fresh approval for the rebased head
	out := h.runWith(t, p3Input())
	if out.Status != Approved || out.HeadSHA != "c1-rebased" || out.BaseSHA != "base2" || rounds != 2 {
		t.Fatalf("outcome %+v rounds %d", out, rounds)
	}
	if strings.Join(heads, ",") != "c1@base,c1-rebased@base2" {
		t.Fatalf("gates ran on %v, want the original then the rebased head", heads)
	}
}

func TestRebaseConflictEscalates(t *testing.T) {
	h := newHarness(t, "RebaseIfMoved")
	h.env.OnActivity(h.a.RebaseIfMoved, mock.Anything, mock.Anything).Return(BaseResult{Moved: true, Conflict: true}, nil)
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.evaluate(nil)
	h.signal(time.Minute, true, "alice")
	if out := h.runWith(t, p3Input()); out.Status != EscalatedHuman || !strings.Contains(out.Reason, "rebases cleanly") {
		t.Fatalf("outcome %+v", out)
	}
}

func TestApprovalPolicyDecides(t *testing.T) {
	h := newHarness(t, "Release", "RecordDecision")
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.evaluate(nil)
	release(h, "high")
	var d decisions
	d.mock(h)
	var seen []string
	h.env.OnActivity(h.a.DecidePolicy, mock.Anything, mock.Anything).Return(func(_ context.Context, in PolicyInput) (PolicyResult, error) {
		seen = append(seen, in.Decision.Reviewer)
		switch {
		case !in.Decision.Passkey:
			return PolicyResult{Reason: "a passkey-signed approval is required", Needed: 2}, nil
		case len(in.Approvals) == 0:
			return PolicyResult{Accept: true, Needed: 2}, nil
		default:
			return PolicyResult{Accept: true, Satisfied: true, Needed: 2}, nil
		}
	})
	h.env.RegisterDelayedCallback(func() { h.env.SignalWorkflow(SignalDecision, Decision{Approve: true, Reviewer: "alice"}) }, time.Minute)
	h.env.RegisterDelayedCallback(func() {
		h.env.SignalWorkflow(SignalDecision, Decision{Approve: true, Reviewer: "alice", Passkey: true})
	}, 2*time.Minute)
	h.env.RegisterDelayedCallback(func() {
		h.env.SignalWorkflow(SignalDecision, Decision{Approve: true, Reviewer: "carol", Passkey: true})
	}, 3*time.Minute)
	in := p3Input()
	in.Settings.UsePolicy = true
	out := h.runWith(t, in)
	if out.Status != Approved || len(seen) != 3 || !strings.Contains(d.list[0].Refused, "passkey") {
		t.Fatalf("outcome %+v seen %v decisions %+v", out, seen, d.list)
	}
}
