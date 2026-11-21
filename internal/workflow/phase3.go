package workflow

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"aegis/internal/gates"
	"go.temporal.io/sdk/temporal"
)

// ---- Phase 3 activities (PLAN.md §10.6) ----

type LeaseInput struct {
	Run   Input    `json:"run"`
	Paths []string `json:"paths"`
}

type LeaseResult struct {
	Granted   bool   `json:"granted"`
	HolderRun string `json:"holder_run,omitempty"`
	Path      string `json:"path,omitempty"`
}

// AcquireLeases claims (or renews) the run's planned paths (P4).
func (a *Activities) AcquireLeases(ctx context.Context, in LeaseInput) (LeaseResult, error) {
	defer keepAlive(ctx)()
	c, err := a.Store.AcquireLeases(ctx, in.Run.TenantID, in.Run.Repo, in.Run.RunID, in.Paths, in.Run.Settings.LeaseTTL)
	if err != nil {
		return LeaseResult{}, err
	}
	if c != nil {
		return LeaseResult{HolderRun: c.HolderRun, Path: c.Path}, nil
	}
	return LeaseResult{Granted: true}, nil
}

// ReleaseLeases drops the run's leases (on finish).
func (a *Activities) ReleaseLeases(ctx context.Context, in Input) error {
	defer keepAlive(ctx)()
	return a.Store.ReleaseLeases(ctx, in.TenantID, in.RunID)
}

// ReviewLoad counts the tenant's other runs awaiting a human (P5).
func (a *Activities) ReviewLoad(ctx context.Context, in Input) (int, error) {
	defer keepAlive(ctx)()
	return a.Store.CountAwaitingReview(ctx, in.TenantID, in.RunID)
}

type BaseInput struct {
	Run       Input         `json:"run"`
	Workspace WorkspaceInfo `json:"workspace"`
	HeadSHA   string        `json:"head_sha"`
	Round     int           `json:"round"`
}

type BaseResult struct {
	Moved    bool   `json:"moved"`
	Conflict bool   `json:"conflict"`
	BaseSHA  string `json:"base_sha"`
	HeadSHA  string `json:"head_sha"`
}

// RebaseIfMoved checks whether the base branch moved since the run's base and,
// if so, rebases the run branch onto the new tip (P9). Idempotent per round.
func (a *Activities) RebaseIfMoved(ctx context.Context, in BaseInput) (BaseResult, error) {
	defer keepAlive(ctx)()
	raw, err := a.Store.Once(ctx, in.Run.TenantID, in.Run.RunID, in.Run.RunID+":rebase:"+itoa(in.Round), func() (json.RawMessage, error) {
		repo, ok := a.Cfg.Repo(in.Run.Repo)
		if !ok {
			return nil, temporal.NewNonRetryableApplicationError("unknown repo "+in.Run.Repo, ErrScenario, nil)
		}
		remote, err := a.Cfg.RepoRemote(repo)
		if err != nil {
			return nil, err
		}
		mirror, err := a.WS.EnsureMirror(ctx, remote)
		if err != nil {
			return nil, err
		}
		ref := in.Run.BaseRef
		if ref == "" {
			ref = repo.DefaultBranch
		}
		tip, err := a.WS.ResolveRef(ctx, mirror, ref)
		if err != nil {
			return nil, err
		}
		wt := in.Workspace.worktree()
		if tip == wt.BaseSHA {
			return json.Marshal(BaseResult{BaseSHA: wt.BaseSHA, HeadSHA: in.HeadSHA})
		}
		head, conflict, err := a.WS.Rebase(ctx, wt, tip)
		if err != nil {
			return nil, err
		}
		res := BaseResult{Moved: true, Conflict: conflict, BaseSHA: tip, HeadSHA: head}
		_ = a.Store.RecordEvent(ctx, in.Run.TenantID, in.Run.RunID, "run.base_moved", map[string]any{
			"old_base": wt.BaseSHA, "new_base": tip, "conflict": conflict, "new_head": head})
		return json.Marshal(res)
	})
	if err != nil {
		return BaseResult{}, err
	}
	var res BaseResult
	return res, json.Unmarshal(raw, &res)
}

// riskTier is "high" when the change touches high-risk paths, a dependency file,
// or a non-test file outside the planned scope (adjacent); otherwise "low".
func riskTier(changed []string, highRisk, adjacent, depFiles []string) string {
	match := func(globs []string, f string) bool {
		for _, g := range globs {
			if ok, _ := doublestar.Match(g, f); ok || g == f {
				return true
			}
		}
		return false
	}
	for _, f := range changed {
		if match(highRisk, f) || match(depFiles, f) || (match(adjacent, f) && !gates.IsTestFile(f)) {
			return "high"
		}
	}
	return "low"
}

// inFreeze returns the freeze window covering t, if any.
func inFreeze(windows []FreezeWindow, t time.Time) (FreezeWindow, bool) {
	for _, w := range windows {
		if !t.Before(w.Start) && t.Before(w.End) {
			return w, true
		}
	}
	return FreezeWindow{}, false
}

func itoa(n int) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
