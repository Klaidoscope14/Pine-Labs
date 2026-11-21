package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"aegis/internal/gaterun"
	"aegis/internal/gates"
	"aegis/internal/workspace"
)

// Gates whose results may be reused for an identical tree (PLAN.md §10.5, L2).
// Security gates are never reused across evaluations (invariant 3).
var reusableGates = []string{"lint", "tests"}

// gateEnv builds the environment for evaluating one commit of a run.
func (a *Activities) gateEnv(ctx context.Context, run Input, wt workspace.Worktree, head string, plan planScope, outRoot string) (gates.Env, error) {
	base, err := a.baseTree(ctx, wt)
	if err != nil {
		return gates.Env{}, err
	}
	changes, err := a.WS.Diff(ctx, wt, wt.BaseSHA, head)
	if err != nil {
		return gates.Env{}, err
	}
	repo, _ := a.Cfg.Repo(run.Repo)
	files, lines := a.diffBudget(plan)
	return gates.Env{
		Sandbox: a.Sandbox, Image: a.Image,
		Timeout:   time.Duration(a.Cfg.Sandbox.TimeoutSeconds) * time.Second,
		MaxOutput: a.Cfg.Sandbox.MaxOutputBytes,
		Repo:      repo, HeadDir: wt.Path, BaseDir: base, BaseSHA: wt.BaseSHA, HeadSHA: head,
		OutRoot:      outRoot,
		BaseCacheDir: filepath.Join(a.Cfg.Path(a.Cfg.Workspace.ArtifactsDir), "base-results", wt.BaseSHA),
		RulesDir:     a.Cfg.Path(a.Cfg.Gates.SemgrepRulesDir),
		HiddenDir:    a.Cfg.Path(a.Cfg.Evals.HiddenTestsDir),
		ReplayDir:    a.Cfg.Path(a.Cfg.Evals.ReplayDir),
		Changes:      changes,
		Planned:      plan.Scope.Planned, Adjacent: plan.Scope.Adjacent,
		Protected:   a.Cfg.Scope.ProtectedPaths,
		BudgetFiles: files, BudgetLines: lines,
		InterfaceChangesDeclared: len(plan.InterfaceChanges) > 0,
	}, nil
}

var (
	imageIDOnce sync.Once
	imageID     string
)

// sandboxImageID is the content digest of the gate image, so a rebuilt image never
// reuses results from the old one.
func (a *Activities) sandboxImageID() string {
	imageIDOnce.Do(func() {
		out, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", a.Image).Output()
		if err == nil {
			imageID = strings.TrimSpace(string(out))
		}
	})
	return imageID
}

// cachePath is where a reusable gate result for this exact tree, image and config lives.
// Empty means caching is unavailable (unknown image digest).
func (a *Activities) cachePath(tree, gate string) string {
	img := a.sandboxImageID()
	if img == "" || tree == "" || a.Cfg.Hash == "" {
		return ""
	}
	key := sha256Hex([]byte(strings.Join([]string{tree, gate, img, a.Cfg.Hash}, "\n")))
	return filepath.Join(a.Cfg.Path(a.Cfg.Workspace.ArtifactsDir), "gate-cache", tree, gate+"-"+key[:16]+".json")
}

func (a *Activities) loadCached(tree, gate string) (gates.Result, bool) {
	p := a.cachePath(tree, gate)
	if p == "" {
		return gates.Result{}, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return gates.Result{}, false
	}
	var r gates.Result
	if json.Unmarshal(data, &r) != nil || r.Gate != gate {
		return gates.Result{}, false
	}
	if r.Metrics == nil {
		r.Metrics = map[string]float64{}
	}
	r.Metrics["reused"] = 1
	return r, true
}

func (a *Activities) storeCached(tree string, r gates.Result) {
	if !slices.Contains(reusableGates, r.Gate) {
		return
	}
	p := a.cachePath(tree, r.Gate)
	if p == "" {
		return
	}
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o750) != nil {
		return
	}
	tmp := fmt.Sprintf("%s.tmp-%d", p, time.Now().UnixNano())
	if os.WriteFile(tmp, data, 0o640) == nil {
		_ = os.Rename(tmp, p)
	}
}

// runGates runs the named gates, reusing lint/tests results already computed for
// this exact tree, and stores fresh reusable results.
func (a *Activities) runGates(ctx context.Context, env gates.Env, names []string, tree string) (gaterun.Outcome, error) {
	cached := map[string]gates.Result{}
	var todo []string
	for _, n := range names {
		if slices.Contains(reusableGates, n) {
			if r, ok := a.loadCached(tree, n); ok {
				cached[n] = r
				continue
			}
		}
		todo = append(todo, n)
	}
	fresh := gaterun.Outcome{Verdict: gates.Pass}
	if len(todo) > 0 {
		var err error
		if fresh, err = gaterun.Run(ctx, env, todo, a.Cfg.Gates.Parallelism); err != nil {
			return gaterun.Outcome{}, err
		}
	}
	byName := map[string]gates.Result{}
	for _, r := range fresh.Results {
		byName[r.Gate] = r
		a.storeCached(tree, r)
	}
	out := gaterun.Outcome{Verdict: gates.Pass}
	for _, n := range names { // keep the configured order
		r, ok := cached[n]
		if !ok {
			r = byName[n]
		}
		out.Results = append(out.Results, r)
		out.Verdict = gates.Worse(out.Verdict, r.Verdict)
	}
	return out, nil
}

// selfCheck returns the developer's pre-finish check: the real scope, lint and
// tests gates on the work in progress. Results are cached by tree, so if the
// developer finishes with exactly this tree the final gates reuse them.
func (a *Activities) selfCheck(run Input, ws WorkspaceInfo, plan planScope, attempt int) func(context.Context) string {
	n := 0
	return func(ctx context.Context) string {
		wt := ws.worktree()
		snap, tree, err := a.WS.Snapshot(ctx, wt)
		if err != nil {
			return "" // the final gates still decide
		}
		n++
		outRoot := filepath.Join(a.runDir(run.RunID), fmt.Sprintf("attempt-%d", attempt), "selfcheck", fmt.Sprintf("%d-%d", n, time.Now().UnixNano()))
		env, err := a.gateEnv(ctx, run, wt, snap, plan, outRoot)
		if err != nil {
			return ""
		}
		names := []string{}
		for _, g := range []string{"scope", "integrity", "lint", "tests"} {
			if slices.Contains(a.Cfg.Gates.Enabled, g) {
				names = append(names, g)
			}
		}
		outcome, err := a.runGates(ctx, env, names, tree)
		if err != nil {
			return ""
		}
		sum := summarize(outcome)
		if sum.Diagnostics == "" {
			return ""
		}
		return sum.Diagnostics
	}
}
