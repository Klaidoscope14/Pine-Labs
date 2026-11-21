package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) *Gateway {
	t.Helper()
	root := t.TempDir()
	write := func(p, s string) {
		abs := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(abs), 0o755)
		os.WriteFile(abs, []byte(s), 0o644)
	}
	write("app/webhooks/handler.py", "def handle():\n    return 1\n")
	write("app/config/payout_auth.py", "THRESHOLD = 10\n")
	write("app/payments/service.py", "x = 1\n")
	write("tests/test_a.py", "def test_a():\n    pass\n")
	write("notes.txt", "card 4111 1111 1111 1111\n")
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]"), 0o644)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top secret"), 0o644)
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "app", "link.txt"))
	return &Gateway{Root: root, Writable: true, MaxRead: 4096, MaxCalls: 30, MaxRunTests: 2,
		Protected: []string{"app/config/payout_auth.py"}, Planned: []string{"app/webhooks/handler.py"}, Adjacent: []string{"tests/**"},
		RunTests: func(context.Context, string) (string, error) { return "3 passed", nil },
		RunLint:  func(context.Context, string) (string, error) { return "All checks passed!", nil }}
}

func run(g *Gateway, tool string, in map[string]any) Outcome {
	raw, _ := json.Marshal(in)
	return g.Execute(context.Background(), tool, raw)
}

func TestPathEscapesAreRefused(t *testing.T) {
	g := setup(t)
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "app/../../x", ".git/config", "app/link.txt"} {
		if out := run(g, "read_file", map[string]any{"path": p, "start_line": 0, "end_line": 0}); !out.IsError {
			t.Errorf("read %q allowed: %s", p, out.Content)
		}
	}
}

func TestProtectedAndOutOfScopeWritesRefused(t *testing.T) {
	g := setup(t)
	run(g, "read_file", map[string]any{"path": "app/config/payout_auth.py", "start_line": 0, "end_line": 0})
	out := run(g, "edit_file", map[string]any{"path": "app/config/payout_auth.py", "old_text": "10", "new_text": "1000"})
	if !out.IsError || !strings.Contains(out.Content, "protected") {
		t.Errorf("protected edit: %+v", out)
	}
	data, _ := os.ReadFile(filepath.Join(g.Root, "app/config/payout_auth.py"))
	if string(data) != "THRESHOLD = 10\n" {
		t.Error("protected file changed")
	}
	run(g, "read_file", map[string]any{"path": "app/payments/service.py", "start_line": 0, "end_line": 0})
	if out := run(g, "edit_file", map[string]any{"path": "app/payments/service.py", "old_text": "x = 1", "new_text": "x = 2"}); !out.IsError || !strings.Contains(out.Content, "raise_conflict") {
		t.Errorf("out-of-scope edit: %+v", out)
	}
	if out := run(g, "create_file", map[string]any{"path": "app/new.py", "content": "x"}); !out.IsError {
		t.Errorf("out-of-scope create allowed")
	}
}

func TestEditRequiresReadAndUniqueMatch(t *testing.T) {
	g := setup(t)
	edit := map[string]any{"path": "app/webhooks/handler.py", "old_text": "return 1", "new_text": "return 2"}
	if out := run(g, "edit_file", edit); !out.IsError || !strings.Contains(out.Content, "read") {
		t.Fatalf("edit before read allowed: %+v", out)
	}
	run(g, "read_file", map[string]any{"path": "app/webhooks/handler.py", "start_line": 0, "end_line": 0})
	if out := run(g, "edit_file", edit); out.IsError {
		t.Fatalf("valid edit refused: %+v", out)
	}
	if out := run(g, "edit_file", map[string]any{"path": "app/webhooks/handler.py", "old_text": "nope", "new_text": "x"}); !out.IsError {
		t.Error("non-matching edit accepted")
	}
	if out := run(g, "create_file", map[string]any{"path": "tests/test_new.py", "content": "def test_x(): pass\n"}); out.IsError {
		t.Errorf("adjacent create refused: %+v", out)
	}
}

func TestReadsAreRedacted(t *testing.T) {
	g := setup(t)
	out := run(g, "read_file", map[string]any{"path": "notes.txt", "start_line": 0, "end_line": 0})
	if strings.Contains(out.Content, "4111") || g.Redactions != 1 {
		t.Errorf("card number reached the model: %q (redactions %d)", out.Content, g.Redactions)
	}
}

func TestLimitsAndLoopBreaker(t *testing.T) {
	g := setup(t)
	in := map[string]any{"dir": "."}
	run(g, "list_files", in)
	run(g, "list_files", in)
	if out := run(g, "list_files", in); !out.Abort {
		t.Error("third identical call did not abort")
	}
	g2 := setup(t)
	g2.MaxCalls = 2
	run(g2, "list_files", map[string]any{"dir": "app"})
	run(g2, "list_files", map[string]any{"dir": "tests"})
	if out := run(g2, "list_files", map[string]any{"dir": "."}); !out.Abort {
		t.Error("call cap not enforced")
	}
	g3 := setup(t)
	run(g3, "run_tests", map[string]any{"selector": ""})
	run(g3, "run_tests", map[string]any{"selector": "tests/test_a.py"})
	if out := run(g3, "run_tests", map[string]any{"selector": ""}); !out.IsError {
		t.Error("run_tests cap not enforced")
	}
	if out := run(g3, "run_tests", map[string]any{"selector": "--co; rm -rf /"}); !out.IsError {
		t.Error("unsafe selector accepted")
	}
}

func TestReadOnlyRole(t *testing.T) {
	g := setup(t)
	g.Writable = false
	if len(g.Tools()) != 3 {
		t.Errorf("read-only role sees %d tools", len(g.Tools()))
	}
	run(g, "read_file", map[string]any{"path": "app/webhooks/handler.py", "start_line": 0, "end_line": 0})
	if out := run(g, "edit_file", map[string]any{"path": "app/webhooks/handler.py", "old_text": "return 1", "new_text": "x"}); !out.IsError {
		t.Error("read-only role could edit")
	}
}

func TestFinishAndConflict(t *testing.T) {
	g := setup(t)
	if out := run(g, "finish", map[string]any{"summary": "done", "decisions": []any{}}); !out.Done || out.Summary != "done" {
		t.Errorf("finish: %+v", out)
	}
	if out := run(g, "raise_conflict", map[string]any{"reason": "needs store.py", "needed_paths": []string{"app/ledger/store.py"}}); !out.Done || out.Conflict == nil {
		t.Errorf("conflict: %+v", out)
	}
}

func TestRunLint(t *testing.T) {
	g := setup(t)
	if out := run(g, "run_lint", map[string]any{"path": "app"}); out.IsError || out.Content != "All checks passed!" {
		t.Fatalf("lint: %+v", out)
	}
	if out := run(g, "run_lint", map[string]any{"path": "../outside"}); !out.IsError {
		t.Error("path escape accepted")
	}
}

func TestFinishSelfCheckRefusesThenAccepts(t *testing.T) {
	g := setup(t)
	g.MaxFinishRefusals = 2
	problems := "- lint (ruff) fails:\nS105"
	g.PreFinish = func(context.Context) string { return problems }
	in := map[string]any{"summary": "done"}
	if out := run(g, "finish", in); !out.IsError || out.Done || !strings.Contains(out.Content, "S105") {
		t.Fatalf("first finish should be refused with the problems: %+v", out)
	}
	problems = ""
	if out := run(g, "finish", in); out.IsError || !out.Done {
		t.Fatalf("clean finish should be accepted: %+v", out)
	}
}

func TestFinishSelfCheckGivesUpAfterLimit(t *testing.T) {
	g := setup(t)
	g.MaxFinishRefusals = 1
	g.PreFinish = func(context.Context) string { return "- tests fail" }
	if out := run(g, "finish", map[string]any{"summary": "a"}); !out.IsError {
		t.Fatal("first finish should be refused")
	}
	// After the limit the gates judge the change; the loop must not spin forever.
	if out := run(g, "finish", map[string]any{"summary": "b"}); !out.Done {
		t.Fatalf("finish after the limit should end the attempt: %+v", out)
	}
}

func TestEditReportsDiffNote(t *testing.T) {
	g := setup(t)
	g.DiffNote = func(context.Context) string { return "change so far: 2 lines" }
	run(g, "read_file", map[string]any{"path": "app/webhooks/handler.py"})
	out := run(g, "edit_file", map[string]any{"path": "app/webhooks/handler.py", "old_text": "return 1", "new_text": "return 2"})
	if out.IsError || !strings.Contains(out.Content, "change so far: 2 lines") {
		t.Fatalf("edit should report the diff size: %+v", out)
	}
}

func TestEditFilesIsAllOrNothing(t *testing.T) {
	g := setup(t)
	run(g, "read_file", map[string]any{"path": "app/webhooks/handler.py"})
	before, _ := os.ReadFile(filepath.Join(g.Root, "app/webhooks/handler.py"))
	// Second edit fails (no match), so the first must not be written either.
	out := run(g, "edit_files", map[string]any{"edits": []map[string]any{
		{"path": "app/webhooks/handler.py", "old_text": "return 1", "new_text": "return 2"},
		{"path": "app/webhooks/handler.py", "old_text": "does not exist", "new_text": "x"},
	}})
	after, _ := os.ReadFile(filepath.Join(g.Root, "app/webhooks/handler.py"))
	if !out.IsError || string(before) != string(after) {
		t.Fatalf("batch with a bad edit must change nothing: %+v", out)
	}
	// Edits apply in order against the batch's own result.
	out = run(g, "edit_files", map[string]any{"edits": []map[string]any{
		{"path": "app/webhooks/handler.py", "old_text": "return 1", "new_text": "return 2"},
		{"path": "app/webhooks/handler.py", "old_text": "return 2", "new_text": "return 3"},
	}})
	after, _ = os.ReadFile(filepath.Join(g.Root, "app/webhooks/handler.py"))
	if out.IsError || !strings.Contains(string(after), "return 3") {
		t.Fatalf("sequential batch failed: %+v %s", out, after)
	}
}

func TestEditFilesEnforcesProtectedAndReadRules(t *testing.T) {
	g := setup(t)
	run(g, "read_file", map[string]any{"path": "app/webhooks/handler.py"})
	if out := run(g, "edit_files", map[string]any{"edits": []map[string]any{
		{"path": "app/webhooks/handler.py", "old_text": "return 1", "new_text": "return 2"},
		{"path": "app/config/payout_auth.py", "old_text": "THRESHOLD = 10", "new_text": "THRESHOLD = 1"},
	}}); !out.IsError {
		t.Fatal("a batch touching a protected file must be refused")
	}
	if out := run(g, "edit_files", map[string]any{"edits": []map[string]any{
		{"path": "tests/test_a.py", "old_text": "pass", "new_text": "assert True"},
	}}); !out.IsError || !strings.Contains(out.Content, "read") {
		t.Fatalf("unread file must be refused: %+v", out)
	}
}

func TestToolConfigWritesRefused(t *testing.T) {
	g := setup(t)
	g.Adjacent = append(g.Adjacent, "**")
	if out := run(g, "create_file", map[string]any{"path": "ruff.toml", "content": "ignore = [\"S\"]\n"}); !out.IsError || !strings.Contains(out.Content, "FIN-014") {
		t.Fatalf("writing linter config must be refused: %+v", out)
	}
}
