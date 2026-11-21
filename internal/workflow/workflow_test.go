package workflow

import (
	"context"
	"encoding/json"
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
	h.env.OnActivity(h.a.CreateRun, mock.Anything, mock.Anything).Return(nil)
	h.env.OnActivity(h.a.RecordTransition, mock.Anything, mock.Anything).Return(nil)
	h.env.OnActivity(h.a.Prepare, mock.Anything, mock.Anything).Return(WorkspaceInfo{RunID: "r1", Path: "/w", Branch: "agent/run-r1", BaseSHA: "base"}, nil)
	if !skipped["Analyze"] {
		h.env.OnActivity(h.a.Analyze, mock.Anything, mock.Anything).Return(AgentOutput{JSON: json.RawMessage(`{}`)}, nil)
	}
	h.env.OnActivity(h.a.Architect, mock.Anything, mock.Anything).Return(AgentOutput{JSON: json.RawMessage(`{}`)}, nil)
	h.env.OnActivity(h.a.Release, mock.Anything, mock.Anything).Return(ReleaseResult{EvidenceHash: "ev"}, nil)
	h.env.OnActivity(h.a.RecordDecision, mock.Anything, mock.Anything).Return(nil)
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
	h.env.ExecuteWorkflow(Run, input())
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

func TestTier2RunsOnlyAfterEarlierTiersPass(t *testing.T) {
	h := newHarness(t)
	h.develop(DevelopResult{CommitSHA: "c1"}, DevelopResult{CommitSHA: "c2"})
	var tiers []string
	h.env.OnActivity(h.a.Evaluate, mock.Anything, mock.Anything).Return(func(_ context.Context, in EvalInput) (EvalSummary, error) {
		tiers = append(tiers, in.Tier)
		if in.Tier == string(GatesTier1) && in.Attempt == 1 {
			return fail("x"), nil
		}
		return EvalSummary{Verdict: "PASS"}, nil
	})
	h.approveAfter(time.Minute, true)
	h.run(t)
	want := []string{"GATES_TIER1", "GATES_TIER1", "GATES_SECURITY", "GATES_TIER2"}
	if len(tiers) != len(want) {
		t.Fatalf("tiers ran %v, want %v", tiers, want)
	}
	for i := range want {
		if tiers[i] != want[i] {
			t.Fatalf("tiers ran %v, want %v", tiers, want)
		}
	}
}
