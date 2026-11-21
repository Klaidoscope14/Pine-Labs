package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

var settings = Settings{MaxCIRounds: 2, Tier1: []string{"scope", "lint", "tests"}, Security: []string{"semgrep", "secrets"},
	Tier2: []string{"hidden_tests", "replay"}, ApprovalExpiry: time.Hour, ActivityTimeout: time.Minute}

func input() Input {
	return Input{RunID: "r1", TenantID: "t1", Repo: "repo", Requirement: "q", Scenario: "s", ConfigHash: "h", Settings: settings}
}

// harness registers mocked activities. evals maps "attempt/tier" to a verdict.
type harness struct {
	suite testsuite.WorkflowTestSuite
	env   *testsuite.TestWorkflowEnvironment
	a     *Activities
}

// newHarness mocks every activity except those named in skip (the test mocks those itself).
func newHarness(t *testing.T, skip ...string) *harness {
	h := &harness{}
	h.env = h.suite.NewTestWorkflowEnvironment()
	h.a = &Activities{}
	h.env.RegisterActivity(h.a)
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	if !skipped["CreateRun"] {
		h.env.OnActivity(h.a.CreateRun, mock.Anything, mock.Anything).Return(nil)
	}
	if !skipped["RecordTransition"] {
		h.env.OnActivity(h.a.RecordTransition, mock.Anything, mock.Anything).Return(nil)
	}
	h.env.OnActivity(h.a.Prepare, mock.Anything, mock.Anything).Return(WorkspaceInfo{RunID: "r1", Path: "/w", Branch: "agent/run-r1", BaseSHA: "base"}, nil)
	if !skipped["Analyze"] {
		h.env.OnActivity(h.a.Analyze, mock.Anything, mock.Anything).Return(AgentOutput{JSON: json.RawMessage(`{}`)}, nil)
	}
	if !skipped["Architect"] {
		h.env.OnActivity(h.a.Architect, mock.Anything, mock.Anything).Return(AgentOutput{JSON: json.RawMessage(`{}`)}, nil)
	}
	if !skipped["Release"] {
		h.env.OnActivity(h.a.Release, mock.Anything, mock.Anything).Return(ReleaseResult{EvidenceHash: "ev"}, nil)
	}
	if !skipped["RecordDecision"] {
		h.env.OnActivity(h.a.RecordDecision, mock.Anything, mock.Anything).Return(nil)
	}
	if !skipped["AcquireLeases"] {
		h.env.OnActivity(h.a.AcquireLeases, mock.Anything, mock.Anything).Return(LeaseResult{Granted: true}, nil)
	}
	h.env.OnActivity(h.a.ReleaseLeases, mock.Anything, mock.Anything).Return(nil)
	if !skipped["ReviewLoad"] {
		h.env.OnActivity(h.a.ReviewLoad, mock.Anything, mock.Anything).Return(0, nil)
	}
	if !skipped["RebaseIfMoved"] {
		h.env.OnActivity(h.a.RebaseIfMoved, mock.Anything, mock.Anything).Return(BaseResult{}, nil)
	}
	h.env.OnActivity(h.a.Cleanup, mock.Anything, mock.Anything).Return(nil)
	return h
}

func (h *harness) develop(results ...DevelopResult) {
	n := 0
	h.env.OnActivity(h.a.Develop, mock.Anything, mock.Anything).Return(func(_ context.Context, in DevelopActivityInput) (DevelopResult, error) {
		r := results[min(n, len(results)-1)]
		n++
		return r, nil
	})
}

func (h *harness) evaluate(verdicts map[string]EvalSummary) {
	h.env.OnActivity(h.a.Evaluate, mock.Anything, mock.Anything).Return(func(_ context.Context, in EvalInput) (EvalSummary, error) {
		if v, ok := verdicts[in.Tier+"/"+string(rune('0'+in.Attempt))]; ok {
			return v, nil
		}
		if v, ok := verdicts[in.Tier]; ok {
			return v, nil
		}
		return EvalSummary{Verdict: "PASS"}, nil
	})
}

func (h *harness) approveAfter(d time.Duration, approve bool) {
	h.env.RegisterDelayedCallback(func() {
		h.env.SignalWorkflow(SignalDecision, Decision{Approve: approve, Reviewer: "alice"})
	}, d)
}

func (h *harness) run(t *testing.T) Outcome {
	t.Helper()
	return h.runWith(t, input())
}

func (h *harness) runWith(t *testing.T, in Input) Outcome {
	t.Helper()
	h.env.ExecuteWorkflow(Run, in)
	if !h.env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out Outcome
	if err := h.env.GetWorkflowResult(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func fail(diag string) EvalSummary { return EvalSummary{Verdict: "REMEDIABLE_FAIL", Diagnostics: diag} }
func block(status Status) EvalSummary {
	return EvalSummary{Verdict: "HARD_BLOCK", BlockStatus: string(status), Reason: "blocked"}
}

func TestHappyPathApproved(t *testing.T) {
	h := newHarness(t)
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.evaluate(nil)
	h.approveAfter(time.Minute, true)
	out := h.run(t)
	if out.Status != Approved || out.Attempts != 1 || out.HeadSHA != "c1" || out.EvidenceHash != "ev" {
		t.Fatalf("outcome %+v", out)
	}
}

func TestRejected(t *testing.T) {
	h := newHarness(t)
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.evaluate(nil)
	h.approveAfter(time.Minute, false)
	if out := h.run(t); out.Status != Rejected {
		t.Fatalf("outcome %+v", out)
	}
}

func TestSelfHealOnSecondAttempt(t *testing.T) {
	h := newHarness(t)
	h.evaluate(map[string]EvalSummary{string(GatesSecurity) + "/1": fail("[semgrep] FIN-001 float")})
	var diagSeen string
	h.env.OnActivity(h.a.Develop, mock.Anything, mock.Anything).Return(func(_ context.Context, in DevelopActivityInput) (DevelopResult, error) {
		if in.Attempt == 2 {
			diagSeen = in.Diagnostics
			return DevelopResult{CommitSHA: "c2"}, nil
		}
		return DevelopResult{CommitSHA: "c1"}, nil
	})
	h.approveAfter(time.Minute, true)
	out := h.run(t)
	if out.Status != Approved || out.Attempts != 2 || out.HeadSHA != "c2" {
		t.Fatalf("outcome %+v", out)
	}
	if diagSeen != "[semgrep] FIN-001 float" {
		t.Errorf("attempt 2 did not receive diagnostics: %q", diagSeen)
	}
}

func TestRetriesExhausted(t *testing.T) {
	h := newHarness(t)
	h.develop(DevelopResult{CommitSHA: "c1"}, DevelopResult{CommitSHA: "c2"})
	h.evaluate(map[string]EvalSummary{string(GatesTier1): fail("[tests] TEST-FAILED")})
	out := h.run(t)
	if out.Status != HaltedRetryExhausted || out.Attempts != settings.MaxCIRounds {
		t.Fatalf("outcome %+v", out)
	}
}

func TestHardBlocksEndTheRun(t *testing.T) {
	for _, status := range []Status{BlockedScope, BlockedSecurityVeto, EscalatedHuman} {
		t.Run(string(status), func(t *testing.T) {
			h := newHarness(t)
			h.develop(DevelopResult{CommitSHA: "c1"})
			h.evaluate(map[string]EvalSummary{string(GatesTier1): block(status)})
			out := h.run(t)
			if out.Status != status || out.Attempts != 1 {
				t.Fatalf("outcome %+v", out)
			}
		})
	}
}

func TestDesignConflictEscalates(t *testing.T) {
	h := newHarness(t)
	h.develop(DevelopResult{Conflict: true, ConflictReason: "needs ledger/store.py"})
	h.evaluate(nil)
	out := h.run(t)
	if out.Status != EscalatedHuman || out.Attempts != 1 {
		t.Fatalf("outcome %+v", out)
	}
}

func TestContractFailureEndsInFailedSchema(t *testing.T) {
	h := newHarness(t, "Analyze")
	h.env.OnActivity(h.a.Analyze, mock.Anything, mock.Anything).Return(AgentOutput{},
		temporal.NewNonRetryableApplicationError("bad", ErrContract, nil))
	out := h.run(t)
	if out.Status != FailedSchema {
		t.Fatalf("outcome %+v", out)
	}
}

func TestApprovalExpires(t *testing.T) {
	h := newHarness(t)
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.evaluate(nil)
	out := h.run(t) // no signal: the timer fires
	if out.Status != Expired {
		t.Fatalf("outcome %+v", out)
	}
}

func TestAllTiersRunEachAttemptAndFailuresAreCombined(t *testing.T) {
	h := newHarness(t)
	var diagSeen string
	n := 0
	h.env.OnActivity(h.a.Develop, mock.Anything, mock.Anything).Return(func(_ context.Context, in DevelopActivityInput) (DevelopResult, error) {
		n++
		if n == 2 {
			diagSeen = in.Diagnostics
		}
		return DevelopResult{CommitSHA: fmt.Sprintf("c%d", n)}, nil
	})
	var mu sync.Mutex
	ran := map[int][]string{}
	h.env.OnActivity(h.a.Evaluate, mock.Anything, mock.Anything).Return(func(_ context.Context, in EvalInput) (EvalSummary, error) {
		mu.Lock()
		ran[in.Attempt] = append(ran[in.Attempt], in.Tier)
		mu.Unlock()
		if in.Attempt == 1 && in.Tier == string(GatesTier1) {
			return fail("[lint] S105 tests/x.py:3"), nil
		}
		if in.Attempt == 1 && in.Tier == string(GatesTier2) {
			return fail("[openapi] breaking change"), nil
		}
		return EvalSummary{Verdict: "PASS"}, nil
	})
	h.approveAfter(time.Minute, true)
	res := h.run(t)
	if res.Status != Approved {
		t.Fatalf("final status %s, want %s", res.Status, Approved)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		got := append([]string(nil), ran[attempt]...)
		sort.Strings(got)
		want := []string{"GATES_SECURITY", "GATES_TIER1", "GATES_TIER2"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("attempt %d ran tiers %v, want all of %v", attempt, got, want)
		}
	}
	if !strings.Contains(diagSeen, "S105") || !strings.Contains(diagSeen, "breaking change") {
		t.Fatalf("retry should see failures from every tier, got %q", diagSeen)
	}
}

func TestHardBlockIsNeverMaskedByEarlierRemediableFailure(t *testing.T) {
	h := newHarness(t)
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.evaluate(map[string]EvalSummary{
		string(GatesTier1):    fail("[lint] E501"),
		string(GatesSecurity): {Verdict: "HARD_BLOCK", BlockStatus: string(BlockedSecurityVeto), Reason: "secrets: leaked key"},
	})
	res := h.run(t)
	if res.Status != BlockedSecurityVeto {
		t.Fatalf("status %s, want %s", res.Status, BlockedSecurityVeto)
	}
}

func TestAgentFailuresMapToStates(t *testing.T) {
	cases := map[string]struct {
		activity string
		errType  string
		want     Status
	}{
		"budget in developer": {"Develop", ErrBudget, BudgetExceeded},
		"refusal in analyst":  {"Analyze", ErrAgentRefused, AgentRefused},
		"plan needs human":    {"Architect", ErrNeedsHuman, EscalatedHuman},
		"data filter tripped": {"Develop", ErrSensitiveData, BlockedSecurityVeto},
		"killed mid-run":      {"Develop", ErrKilled, Killed},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, c.activity)
			appErr := temporal.NewNonRetryableApplicationError("x", c.errType, nil)
			switch c.activity {
			case "Analyze":
				h.env.OnActivity(h.a.Analyze, mock.Anything, mock.Anything).Return(AgentOutput{}, appErr)
			case "Architect":
				h.env.OnActivity(h.a.Architect, mock.Anything, mock.Anything).Return(AgentOutput{}, appErr)
			case "Develop":
				h.env.OnActivity(h.a.Develop, mock.Anything, mock.Anything).Return(DevelopResult{}, appErr)
			}
			h.evaluate(nil)
			if out := h.run(t); out.Status != c.want {
				t.Fatalf("status %s, want %s", out.Status, c.want)
			}
		})
	}
}

func TestKillSwitchRefusesNewRun(t *testing.T) {
	h := newHarness(t, "CreateRun")
	h.env.OnActivity(h.a.CreateRun, mock.Anything, mock.Anything).Return(
		temporal.NewNonRetryableApplicationError("kill switch is on", ErrKilled, nil))
	if out := h.run(t); out.Status != Killed {
		t.Fatalf("status %s", out.Status)
	}
}

func TestProviderOutageIsRetried(t *testing.T) {
	h := newHarness(t)
	calls := 0
	h.env.OnActivity(h.a.Develop, mock.Anything, mock.Anything).Return(func(context.Context, DevelopActivityInput) (DevelopResult, error) {
		calls++
		if calls < 3 {
			return DevelopResult{}, errors.New("all model providers unavailable")
		}
		return DevelopResult{CommitSHA: "c1"}, nil
	})
	h.evaluate(nil)
	h.approveAfter(time.Hour, true)
	if out := h.run(t); out.Status != Approved || calls != 3 {
		t.Fatalf("status %s after %d calls", out.Status, calls)
	}
}
