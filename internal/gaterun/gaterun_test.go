package gaterun

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aegis/internal/config"
	"aegis/internal/gates"
	"aegis/internal/sandbox"
	"aegis/internal/workspace"
)

// These tests run every gate for real in the sandbox. They need Docker and the
// sandbox image (AEGIS_SANDBOX_IMAGE), and are skipped otherwise.

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	return filepath.Clean(filepath.Join(dir, "..", ".."))
}

type fixture struct {
	cfg  *config.Config
	ws   workspace.Manager
	mir  string
	base string
	env  gates.Env
	root string
}

func setup(t *testing.T) fixture {
	t.Helper()
	image := os.Getenv("AEGIS_SANDBOX_IMAGE")
	if image == "" {
		t.Skip("AEGIS_SANDBOX_IMAGE not set")
	}
	root := repoRoot(t)
	cfg, err := config.LoadFile(filepath.Join(root, "config.local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// A throwaway "remote" built from the sample sources.
	src := t.TempDir()
	cp := exec.Command("cp", "-R", filepath.Join(root, "samples", "pine-settlements-svc")+"/.", src)
	if out, err := cp.CombinedOutput(); err != nil {
		t.Fatalf("copy sample: %v %s", err, out)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "base"}} {
		c := exec.Command("git", args...)
		c.Dir = src
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	ctx := context.Background()
	ws := workspace.Manager{MirrorsDir: t.TempDir(), RunsDir: t.TempDir(), AuthorName: "aegis", AuthorMail: "aegis@local"}
	mir, err := ws.EnsureMirror(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	base, err := ws.ResolveRef(ctx, mir, "main")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := sandbox.PolicyFromConfig(cfg.Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := cfg.Repo("pine-settlements-svc")
	var plan struct {
		Scope struct {
			Planned  []string `json:"planned"`
			Adjacent []string `json:"adjacent"`
		} `json:"scope"`
		DiffBudget struct{ Files, Lines int } `json:"diff_budget"`
	}
	raw, _ := os.ReadFile(filepath.Join(root, "evals/scenarios/_common/architecture_plan.json"))
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	env := gates.Env{
		Sandbox:      sandbox.Docker{Policy: policy, MaxOutputBytes: cfg.Sandbox.MaxOutputBytes},
		Image:        image,
		Timeout:      time.Duration(cfg.Sandbox.TimeoutSeconds) * time.Second,
		MaxOutput:    cfg.Sandbox.MaxOutputBytes,
		Repo:         repo,
		BaseSHA:      base,
		BaseCacheDir: t.TempDir(),
		RulesDir:     cfg.Path(cfg.Gates.SemgrepRulesDir),
		HiddenDir:    cfg.Path(cfg.Evals.HiddenTestsDir),
		ReplayDir:    cfg.Path(cfg.Evals.ReplayDir),
		Planned:      plan.Scope.Planned,
		Adjacent:     plan.Scope.Adjacent,
		Protected:    cfg.Scope.ProtectedPaths,
		BudgetFiles:  plan.DiffBudget.Files,
		BudgetLines:  plan.DiffBudget.Lines,
	}
	return fixture{cfg: cfg, ws: ws, mir: mir, base: base, env: env, root: root}
}

func (f fixture) evaluate(t *testing.T, name, patchFile string) map[string]gates.Result {
	t.Helper()
	ctx := context.Background()
	wt, err := f.ws.CreateWorktree(ctx, f.mir, name, f.base)
	if err != nil {
		t.Fatal(err)
	}
	head := f.base
	if patchFile != "" {
		patch, err := os.ReadFile(filepath.Join(f.root, "evals/scenarios", patchFile))
		if err != nil {
			t.Fatal(err)
		}
		if head, err = f.ws.ApplyAndCommit(ctx, wt, patch, "test", name); err != nil {
			t.Fatal(err)
		}
	}
	env := f.env
	env.HeadDir, env.HeadSHA = wt.Path, head
	env.BaseDir = filepath.Join(t.TempDir(), "base")
	if err := f.ws.ExportTree(ctx, wt, f.base, env.BaseDir); err != nil {
		t.Fatal(err)
	}
	env.OutRoot = t.TempDir()
	if env.Changes, err = f.ws.Diff(ctx, wt, f.base, head); err != nil {
		t.Fatal(err)
	}
	all := append(append(append([]string{}, f.cfg.Gates.Tier1...), f.cfg.Gates.Security...), f.cfg.Gates.Tier2...)
	if patchFile == "" {
		all = all[1:] // scope on an empty change is meaningless
	}
	out, err := Run(ctx, env, all, f.cfg.Gates.Parallelism)
	if err != nil {
		t.Fatal(err)
	}
	res := map[string]gates.Result{}
	for _, r := range out.Results {
		res[r.Gate] = r
	}
	return res
}

func expectVerdicts(t *testing.T, got map[string]gates.Result, want map[string]gates.Verdict) {
	t.Helper()
	for gate, v := range want {
		if got[gate].Verdict != v {
			js, _ := json.MarshalIndent(got[gate], "", "  ")
			t.Errorf("%s: verdict %s, want %s\n%s", gate, got[gate].Verdict, v, js)
		}
	}
}

func hasFinding(r gates.Result, rule string, preExisting bool) bool {
	for _, f := range r.Findings {
		if strings.HasPrefix(f.RuleID, rule) && f.PreExisting == preExisting {
			return true
		}
	}
	return false
}

func TestGatesOnBaseCode(t *testing.T) {
	f := setup(t)
	got := f.evaluate(t, "base", "")
	expectVerdicts(t, got, map[string]gates.Verdict{
		"lint": gates.Pass, "tests": gates.Pass, "semgrep": gates.Pass, "secrets": gates.Pass,
		"dependencies": gates.Pass, "openapi": gates.Pass,
		"hidden_tests": gates.RemediableFail, // the planted bug
		"replay":       gates.RemediableFail, // duplicates double-credit
	})
	if !hasFinding(got["semgrep"], "FIN-004", true) {
		t.Errorf("FIN-004 should flag the base webhook handler as pre-existing: %+v", got["semgrep"].Findings)
	}
}

func TestGatesOnScenarioPatches(t *testing.T) {
	f := setup(t)
	pass := map[string]gates.Verdict{"lint": gates.Pass, "tests": gates.Pass, "semgrep": gates.Pass,
		"secrets": gates.Pass, "hidden_tests": gates.Pass, "replay": gates.Pass, "openapi": gates.Pass}

	t.Run("happy", func(t *testing.T) {
		got := f.evaluate(t, "happy", "happy/attempt-1.patch")
		expectVerdicts(t, got, pass)
		expectVerdicts(t, got, map[string]gates.Verdict{"scope": gates.Flag}) // tests/** is adjacent
		if len(got["semgrep"].Resolved) == 0 || got["semgrep"].Resolved[0].RuleID != "FIN-004" {
			t.Errorf("FIN-004 should be resolved by the fix: %+v", got["semgrep"].Resolved)
		}
	})
	t.Run("float", func(t *testing.T) {
		got := f.evaluate(t, "float", "self-heal/attempt-1.patch")
		expectVerdicts(t, got, map[string]gates.Verdict{"semgrep": gates.RemediableFail, "tests": gates.Pass})
		if !hasFinding(got["semgrep"], "FIN-001", false) {
			t.Errorf("FIN-001 not reported: %+v", got["semgrep"].Findings)
		}
	})
	t.Run("broken-test", func(t *testing.T) {
		got := f.evaluate(t, "broken", "retry-exhausted/attempt-1.patch")
		expectVerdicts(t, got, map[string]gates.Verdict{"tests": gates.RemediableFail})
	})
	t.Run("protected", func(t *testing.T) {
		got := f.evaluate(t, "protected", "protected-file/attempt-1.patch")
		expectVerdicts(t, got, map[string]gates.Verdict{"scope": gates.HardBlock})
	})
	t.Run("secret", func(t *testing.T) {
		got := f.evaluate(t, "secret", "secret/attempt-1.patch")
		expectVerdicts(t, got, map[string]gates.Verdict{"secrets": gates.HardBlock})
	})
}
