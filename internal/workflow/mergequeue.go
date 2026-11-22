package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// Merge queue (PLAN.md §10.8): one long-running workflow per repo merges approved
// runs one at a time. Each is rebased onto the current tip of main and re-tested
// (tier 1 + security) before main is fast-forwarded, so two changes that each
// passed alone can't break main together.

const (
	// MergeQueueName is the registered name of the merge queue workflow.
	MergeQueueName = "AegisMergeQueue"
	// SignalEnqueue adds an approved run to the queue.
	SignalEnqueue = "enqueue"

	Merged      Status = "MERGED"
	MergeFailed Status = "MERGE_FAILED"
)

// MergeQueueID is the workflow id of a repo's queue.
func MergeQueueID(tenant, repo string) string { return "merge-queue-" + tenant + "-" + repo }

type MergeRequest struct {
	Run       Input           `json:"run"`
	Workspace WorkspaceInfo   `json:"workspace"`
	HeadSHA   string          `json:"head_sha"`
	Plan      json.RawMessage `json:"plan"`
}

type MergeResult struct {
	Status    Status `json:"status"`
	MergedSHA string `json:"merged_sha,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// mergeBatch bounds history: the queue continues as new after this many merges.
const mergeBatch = 50

// MergeQueue processes enqueued runs in order, forever (it ends after a day idle;
// the next enqueue starts it again).
func MergeQueue(ctx workflow.Context, repo string, carry []MergeRequest) error {
	var a *Activities
	queue := carry
	ch := workflow.GetSignalChannel(ctx, SignalEnqueue)
	done := 0
	for {
		for {
			var r MergeRequest
			if !ch.ReceiveAsync(&r) {
				break
			}
			queue = append(queue, r)
		}
		if len(queue) == 0 {
			idle := false
			sel := workflow.NewSelector(ctx)
			sel.AddReceive(ch, func(c workflow.ReceiveChannel, _ bool) {
				var r MergeRequest
				c.Receive(ctx, &r)
				queue = append(queue, r)
			})
			tctx, cancel := workflow.WithCancel(ctx)
			sel.AddFuture(workflow.NewTimer(tctx, 24*time.Hour), func(workflow.Future) { idle = true })
			sel.Select(ctx)
			cancel()
			if idle {
				return nil
			}
			continue
		}
		req := queue[0]
		queue = queue[1:]
		actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: req.Run.Settings.ActivityTimeout,
			HeartbeatTimeout:    req.Run.Settings.HeartbeatTimeout,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3, NonRetryableErrorTypes: []string{ErrScenario}},
		})
		var res MergeResult
		if err := workflow.ExecuteActivity(actx, a.MergeRun, req).Get(actx, &res); err != nil {
			// Infrastructure failure after retries: record it, never merge silently.
			_ = workflow.ExecuteActivity(actx, a.RecordMergeOutcome, req, MergeResult{Status: MergeFailed, Reason: err.Error()}).Get(actx, nil)
		}
		done++
		if done >= mergeBatch {
			for {
				var r MergeRequest
				if !ch.ReceiveAsync(&r) {
					break
				}
				queue = append(queue, r)
			}
			return workflow.NewContinueAsNewError(ctx, MergeQueueName, repo, queue)
		}
	}
}

// EnqueueMerge adds an approved run to its repo's queue (starting the queue if needed).
func (a *Activities) EnqueueMerge(ctx context.Context, req MergeRequest) error {
	defer keepAlive(ctx)()
	if a.Temporal == nil {
		return temporal.NewNonRetryableApplicationError("merge queue needs a Temporal client", ErrScenario, nil)
	}
	_, err := a.Temporal.SignalWithStartWorkflow(ctx, MergeQueueID(req.Run.TenantID, req.Run.Repo), SignalEnqueue, req,
		client.StartWorkflowOptions{ID: MergeQueueID(req.Run.TenantID, req.Run.Repo), TaskQueue: activity.GetInfo(ctx).TaskQueue},
		MergeQueueName, req.Run.Repo, []MergeRequest(nil))
	if err == nil {
		_ = a.Store.RecordEvent(ctx, req.Run.TenantID, req.Run.RunID, "run.merge_queued", map[string]any{"head_sha": req.HeadSHA})
	}
	return err
}

// RecordMergeOutcome writes a merge result to the ledger.
func (a *Activities) RecordMergeOutcome(ctx context.Context, req MergeRequest, res MergeResult) error {
	defer keepAlive(ctx)()
	return a.Store.RecordMerge(ctx, req.Run.TenantID, req.Run.RunID, string(res.Status), res.MergedSHA, res.Reason)
}

// MergeRun rebases one approved run onto the current tip, re-runs tier 1 and the
// security tier, and fast-forwards main. Idempotent per run.
func (a *Activities) MergeRun(ctx context.Context, req MergeRequest) (MergeResult, error) {
	defer keepAlive(ctx)()
	raw, err := a.Store.Once(ctx, req.Run.TenantID, req.Run.RunID, req.Run.RunID+":merge", func() (json.RawMessage, error) {
		res, err := a.merge(ctx, req)
		if err != nil {
			return nil, err
		}
		if err := a.RecordMergeOutcome(ctx, req, res); err != nil {
			return nil, err
		}
		return json.Marshal(res)
	})
	if err != nil {
		return MergeResult{}, err
	}
	var res MergeResult
	return res, json.Unmarshal(raw, &res)
}

func (a *Activities) merge(ctx context.Context, req MergeRequest) (MergeResult, error) {
	fail := func(reason string) (MergeResult, error) { return MergeResult{Status: MergeFailed, Reason: reason}, nil }
	if on, reason, err := a.Store.KillActive(ctx, req.Run.TenantID); err != nil {
		return MergeResult{}, err
	} else if on {
		return fail("kill switch on: " + reason)
	}
	repo, ok := a.Cfg.Repo(req.Run.Repo)
	if !ok {
		return MergeResult{}, temporal.NewNonRetryableApplicationError("unknown repo "+req.Run.Repo, ErrScenario, nil)
	}
	remote, err := a.Cfg.RepoRemote(repo)
	if err != nil {
		return MergeResult{}, err
	}
	var plan planScope
	_ = json.Unmarshal(req.Plan, &plan)
	for try := 0; try < 3; try++ {
		mirror, err := a.WS.EnsureMirror(ctx, remote)
		if err != nil {
			return MergeResult{}, err
		}
		tip, err := a.WS.ResolveRef(ctx, mirror, repo.DefaultBranch)
		if err != nil {
			return MergeResult{}, err
		}
		wt, err := a.WS.MergeWorktree(ctx, mirror, req.Run.RunID, req.Workspace.Branch, req.Workspace.BaseSHA)
		if err != nil {
			return MergeResult{}, err
		}
		head := req.HeadSHA
		if tip != req.Workspace.BaseSHA {
			h, conflict, err := a.WS.Rebase(ctx, wt, tip)
			if err != nil {
				_ = a.WS.RemoveWorktree(ctx, wt)
				return MergeResult{}, err
			}
			if conflict {
				_ = a.WS.RemoveWorktree(ctx, wt)
				return fail("conflicts with the current main; needs a new run")
			}
			head, wt.BaseSHA = h, tip
		}
		// Re-test the exact commit that will land.
		ws := WorkspaceInfo{RunID: req.Run.RunID, Mirror: wt.Mirror, Path: wt.Path, Branch: wt.Branch, BaseSHA: wt.BaseSHA}
		outRoot := filepath.Join(a.runDir(req.Run.RunID), "merge", fmt.Sprintf("try-%d-%d", try, time.Now().UnixNano()))
		env, err := a.gateEnv(ctx, req.Run, ws.worktree(), head, plan, outRoot)
		if err != nil {
			_ = a.WS.RemoveWorktree(ctx, wt)
			return MergeResult{}, err
		}
		tree, _ := a.WS.TreeOf(ctx, wt, head)
		outcome, err := a.runGates(ctx, env, append(append([]string{}, req.Run.Settings.Tier1...), req.Run.Settings.Security...), tree)
		if err != nil {
			_ = a.WS.RemoveWorktree(ctx, wt)
			return MergeResult{}, err
		}
		if sum := summarize(outcome); sum.Verdict == "REMEDIABLE_FAIL" || sum.Verdict == "HARD_BLOCK" {
			_ = a.WS.RemoveWorktree(ctx, wt)
			first, _, _ := strings.Cut(sum.Diagnostics, "\n")
			return fail("checks fail on the current main: " + first)
		}
		moved, err := a.WS.PushMain(ctx, wt, remote, repo.DefaultBranch)
		_ = a.WS.RemoveWorktree(ctx, wt)
		if err != nil {
			return MergeResult{}, err
		}
		if !moved {
			return MergeResult{Status: Merged, MergedSHA: head}, nil
		}
		// main moved between fetch and push: go round again on the new tip.
	}
	return fail("main kept moving; try again")
}
