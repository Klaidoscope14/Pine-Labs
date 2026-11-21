package llmagents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestDiffStatCountsEditsAndNewFiles(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "app.py"), []byte("a = 1\nb = 2\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	base := git(t, dir, "rev-parse", "HEAD")

	os.WriteFile(filepath.Join(dir, "app.py"), []byte("a = 1\nb = 3\n"), 0o644) // 1 added + 1 deleted
	os.MkdirAll(filepath.Join(dir, "tests"), 0o755)
	os.WriteFile(filepath.Join(dir, "tests", "test_app.py"), []byte("x\ny\nz\n"), 0o644) // new: 3 added

	stats, err := diffStat(context.Background(), dir, base)
	if err != nil {
		t.Fatal(err)
	}
	lines, tests := totals(stats)
	if lines != 5 || tests != 3 || len(stats) != 2 || stats[0].Path != "tests/test_app.py" {
		t.Fatalf("stats %+v lines %d tests %d", stats, lines, tests)
	}
	if note := diffNote(stats, 8, 4); !strings.Contains(note, "OVER by 1") {
		t.Errorf("note should flag the overrun: %s", note)
	}
	if p := budgetProblem(stats, 8, 4); !strings.Contains(p, "5 lines changed, budget is 4") || !strings.Contains(p, "tests/test_app.py (3)") {
		t.Errorf("problem: %s", p)
	}
	if p := budgetProblem(stats, 8, 10); p != "" {
		t.Errorf("within budget should report nothing: %s", p)
	}
}

func TestExitCode(t *testing.T) {
	if exitCode("exit code 1\nboom") != 1 || exitCode("exit code 0\n") != 0 || exitCode("garbage") != -1 {
		t.Fatal("exitCode parsing")
	}
}

func TestPlannedContextIncludesPlannedFilesAndTheirTests(t *testing.T) {
	dir := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
	}
	write("app/webhooks/handler.py", "def handle(): pass\n")
	write("app/config/payout_auth.py", "THRESHOLD = 1\n")
	write("tests/test_handler.py", "from app.webhooks.handler import handle\n")
	write("tests/test_other.py", "def test_x(): pass\n")
	pack, full := plannedContext(dir, []string{"app/webhooks/**", "app/config/**"}, []string{"app/config/payout_auth.py"}, 10000)
	if !strings.Contains(pack, "app/webhooks/handler.py") || !strings.Contains(pack, "tests/test_handler.py") {
		t.Fatalf("missing planned file or its test:\n%s", pack)
	}
	if strings.Contains(pack, "payout_auth") || strings.Contains(pack, "test_other") {
		t.Fatalf("protected or unrelated file included:\n%s", pack)
	}
	if len(full) != 2 {
		t.Fatalf("full = %v", full)
	}
	// Over budget: listed as omitted, never marked as read.
	pack, full = plannedContext(dir, []string{"app/webhooks/**"}, nil, 5)
	if !strings.Contains(pack, "omitted") || len(full) != 0 {
		t.Fatalf("budget not enforced: %v\n%s", full, pack)
	}
}
