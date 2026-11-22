package workflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func TestMergeQueueMergesInOrderOneAtATime(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	var mu sync.Mutex
	var order []string
	running, peak := 0, 0
	env.OnActivity(a.MergeRun, mock.Anything, mock.Anything).Return(func(_ context.Context, r MergeRequest) (MergeResult, error) {
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		order = append(order, r.Run.RunID)
		mu.Unlock()
		defer func() { mu.Lock(); running--; mu.Unlock() }()
		if r.Run.RunID == "r2" {
			return MergeResult{}, temporal.NewNonRetryableApplicationError("disk full", ErrScenario, errors.New("x"))
		}
		return MergeResult{Status: Merged, MergedSHA: "m-" + r.Run.RunID}, nil
	})
	var recorded []MergeResult
	env.OnActivity(a.RecordMergeOutcome, mock.Anything, mock.Anything, mock.Anything).Return(func(_ context.Context, r MergeRequest, res MergeResult) error {
		recorded = append(recorded, res)
		return nil
	})
	req := func(id string) MergeRequest {
		return MergeRequest{Run: Input{RunID: id, Settings: Settings{ActivityTimeout: time.Minute}}}
	}
	for i, id := range []string{"r1", "r2", "r3"} {
		id := id
		env.RegisterDelayedCallback(func() { env.SignalWorkflow(SignalEnqueue, req(id)) }, time.Duration(i+1)*time.Second)
	}
	env.ExecuteWorkflow(MergeQueue, "repo", []MergeRequest(nil)) // ends after a day idle
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("queue did not end cleanly: %v", env.GetWorkflowError())
	}
	if len(order) != 3 || order[0] != "r1" || order[1] != "r2" || order[2] != "r3" || peak != 1 {
		t.Fatalf("order %v peak %d", order, peak)
	}
	if len(recorded) != 1 || recorded[0].Status != MergeFailed {
		t.Fatalf("an infrastructure failure must be recorded as MERGE_FAILED: %+v", recorded)
	}
}

func TestApprovedRunIsEnqueued(t *testing.T) {
	h := newHarness(t)
	h.develop(DevelopResult{CommitSHA: "c1"})
	h.evaluate(nil)
	var got []MergeRequest
	h.env.OnActivity(h.a.EnqueueMerge, mock.Anything, mock.Anything).Return(func(_ context.Context, r MergeRequest) error {
		got = append(got, r)
		return nil
	})
	h.approveAfter(time.Minute, true)
	in := p3Input()
	in.Settings.MergeQueue = true
	out := h.runWith(t, in)
	if out.Status != Approved || len(got) != 1 || got[0].HeadSHA != "c1" {
		t.Fatalf("outcome %+v enqueued %+v", out, got)
	}
}
