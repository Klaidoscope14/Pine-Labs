package gaterun

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"aegis/internal/config"
	"aegis/internal/gates"
	"aegis/internal/sandbox"
	"aegis/internal/workspace"
)

// recorder is a fake sandbox that records every spec it is asked to run.
type recorder struct {
	mu    sync.Mutex
	specs map[string][]sandbox.Spec // by gate
	gate  string
}

func (r *recorder) Run(_ context.Context, s sandbox.Spec) (sandbox.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs[r.gate] = append(r.specs[r.gate], s)
	return sandbox.Result{ExitCode: 0, Stdout: []byte("{}")}, nil
}

// Exit criterion 6: the hidden oracle and replay harness are mounted only into
// their own gates, always read-only, and nothing else in evals/ is ever mounted.
func TestEvalsAreMountedOnlyWhereNeeded(t *testing.T) {
	dir := t.TempDir()
	rec := &recorder{specs: map[string][]sandbox.Spec{}}
	env := gates.Env{
		Sandbox: rec, Image: "img", Timeout: time.Second, MaxOutput: 1 << 20,
		Repo:    config.Repo{SourceDirs: []string{"app"}, OpenAPIExport: []string{"python", "x.py"}},
		HeadDir: dir, BaseDir: dir, OutRoot: t.TempDir(),
		RulesDir: "/abs/rules", HiddenDir: "/abs/evals/hidden", ReplayDir: "/abs/evals/replay",
		Changes: []workspace.Change{{Path: "app/a.py", Status: "M"}},
	}
	for name, fn := range registry {
		rec.gate = name
		_, _ = fn(context.Background(), env) // results don't matter; mounts do
	}
	for gate, specs := range rec.specs {
		for _, s := range specs {
			for _, m := range s.Mounts {
				switch {
				case m.HostPath == env.HiddenDir && gate != "hidden_tests":
					t.Errorf("gate %s mounted the hidden tests", gate)
				case m.HostPath == env.ReplayDir && gate != "replay":
					t.Errorf("gate %s mounted the replay harness", gate)
				case strings.Contains(m.HostPath, "/evals/") && !m.ReadOnly:
					t.Errorf("gate %s mounted %s writable", gate, m.HostPath)
				case m.ContainerPath == "/work" && !m.ReadOnly:
					t.Errorf("gate %s mounted the code under test writable", gate)
				}
			}
		}
	}
	if len(rec.specs["hidden_tests"]) == 0 || len(rec.specs["replay"]) == 0 {
		t.Fatalf("expected hidden_tests and replay to run: %v", rec.specs)
	}
}
