package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRemote makes a bare repo with one commit on main.
func newRemote(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	run(t, src, "init", "-q", "-b", "main")
	os.MkdirAll(filepath.Join(src, "app"), 0o755)
	os.WriteFile(filepath.Join(src, "app", "a.py"), []byte("x = 1\n"), 0o644)
	os.WriteFile(filepath.Join(src, "old.txt"), []byte("old\n"), 0o644)
	run(t, src, "add", ".")
	run(t, src, "commit", "-q", "-m", "init")
	remote := filepath.Join(t.TempDir(), "remote.git")
	run(t, src, "clone", "-q", "--bare", src, remote)
	return remote
}

const patch = `diff --git a/app/a.py b/app/a.py
--- a/app/a.py
+++ b/app/a.py
@@ -1 +1,2 @@
 x = 1
+y = 2
diff --git a/old.txt b/new.txt
similarity index 100%
rename from old.txt
rename to new.txt
`

func TestWorktreeLifecycle(t *testing.T) {
	ctx := context.Background()
	m := Manager{MirrorsDir: t.TempDir(), RunsDir: t.TempDir(), AuthorName: "aegis", AuthorMail: "aegis@local"}
	mirror, err := m.EnsureMirror(ctx, newRemote(t))
	if err != nil {
		t.Fatal(err)
	}
	base, err := m.ResolveRef(ctx, mirror, "main")
	if err != nil {
		t.Fatal(err)
	}
	wt, err := m.CreateWorktree(ctx, mirror, "r1", base)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := m.CreateWorktree(ctx, mirror, "r1", base); err != nil || again.Path != wt.Path {
		t.Fatalf("CreateWorktree not idempotent: %v", err)
	}
	sha, err := m.ApplyAndCommit(ctx, wt, []byte(patch), "change", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	// Retried activity: same key must not create a second commit.
	sha2, err := m.ApplyAndCommit(ctx, wt, []byte(patch), "change", "key-1")
	if err != nil || sha2 != sha {
		t.Fatalf("retry produced %s (err %v), want %s", sha2, err, sha)
	}
	if n, _ := m.CommitCount(ctx, wt); n != 1 {
		t.Fatalf("commit count = %d, want 1", n)
	}
	changes, err := m.Diff(ctx, wt, base, sha)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Change{}
	for _, c := range changes {
		got[c.Path] = c
	}
	if c := got["app/a.py"]; c.Status != "M" || c.Added != 1 {
		t.Errorf("a.py change = %+v", c)
	}
	if c := got["new.txt"]; c.Status != "R" || c.OldPath != "old.txt" {
		t.Errorf("rename change = %+v", c)
	}
	if err := m.RemoveWorktree(ctx, wt); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentWorktreesOnOneMirror(t *testing.T) {
	ctx := context.Background()
	m := Manager{MirrorsDir: t.TempDir(), RunsDir: t.TempDir(), AuthorName: "aegis", AuthorMail: "aegis@local"}
	remote := newRemote(t)
	mirror, err := m.EnsureMirror(ctx, remote)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := m.ResolveRef(ctx, mirror, "main")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.EnsureMirror(ctx, remote); err != nil {
				errs <- err
				return
			}
			wt, err := m.CreateWorktree(ctx, mirror, "c"+string(rune('a'+i)), base)
			if err != nil {
				errs <- err
				return
			}
			if _, err := m.ApplyAndCommit(ctx, wt, []byte(patch), "c", "k"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestRejectsUnsafeInputs(t *testing.T) {
	m := Manager{MirrorsDir: t.TempDir(), RunsDir: t.TempDir()}
	if _, err := m.CreateWorktree(context.Background(), "x", "../escape", strings.Repeat("a", 40)); err == nil {
		t.Error("path-traversal run id accepted")
	}
	if _, err := m.ResolveRef(context.Background(), t.TempDir(), "--upload-pack=evil"); err == nil {
		t.Error("option-like ref accepted")
	}
}

func TestCommitWorkingIsIdempotent(t *testing.T) {
	ctx := context.Background()
	m := Manager{MirrorsDir: t.TempDir(), RunsDir: t.TempDir(), AuthorName: "aegis", AuthorMail: "aegis@local"}
	mirror, _ := m.EnsureMirror(ctx, newRemote(t))
	base, _ := m.ResolveRef(ctx, mirror, "main")
	wt, err := m.CreateWorktree(ctx, mirror, "w1", base)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt.Path, "app", "a.py"), []byte("x = 2\n"), 0o644)
	os.WriteFile(filepath.Join(wt.Path, "app", "new.py"), []byte("y = 1\n"), 0o644)
	sha, err := m.CommitWorking(ctx, wt, "agent change", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := m.CommitWorking(ctx, wt, "agent change", "k1"); again != sha {
		t.Fatal("retry made a second commit")
	}
	changes, _ := m.Diff(ctx, wt, base, sha)
	if len(changes) != 2 {
		t.Fatalf("changes = %+v", changes)
	}
	os.WriteFile(filepath.Join(wt.Path, "junk.txt"), []byte("x"), 0o644)
	if err := m.ResetClean(ctx, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "junk.txt")); err == nil {
		t.Error("ResetClean left untracked files")
	}
}

// Regression: refreshing the mirror must never delete another run's branch.
func TestRefreshKeepsRunBranches(t *testing.T) {
	ctx := context.Background()
	m := Manager{MirrorsDir: t.TempDir(), RunsDir: t.TempDir(), AuthorName: "aegis", AuthorMail: "aegis@local"}
	remote := newRemote(t)
	mirror, _ := m.EnsureMirror(ctx, remote)
	base, _ := m.ResolveRef(ctx, mirror, "main")
	wt, _ := m.CreateWorktree(ctx, mirror, "keep", base)
	if _, err := m.ApplyAndCommit(ctx, wt, []byte(patch), "c", "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsureMirror(ctx, remote); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Head(ctx, wt); err != nil {
		t.Fatalf("run branch lost after refresh: %v", err)
	}
	if n, err := m.CommitCount(ctx, wt); err != nil || n != 1 {
		t.Fatalf("run branch damaged: %d %v", n, err)
	}
}

// Regression (found on Linux/EC2): the exported base tree is mounted into
// sandboxes running as an unprivileged user, so it must be world-readable.
func TestExportTreeIsReadableBySandboxUser(t *testing.T) {
	ctx := context.Background()
	m := Manager{MirrorsDir: t.TempDir(), RunsDir: t.TempDir(), AuthorName: "aegis", AuthorMail: "aegis@local"}
	mirror, _ := m.EnsureMirror(ctx, newRemote(t))
	base, _ := m.ResolveRef(ctx, mirror, "main")
	wt, err := m.CreateWorktree(ctx, mirror, "w1", base)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "base")
	if err := m.ExportTree(ctx, wt, base, dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm()&0o005 != 0o005 {
		t.Fatalf("base tree mode %v, want others r-x", info.Mode().Perm())
	}
}

// Snapshot captures work in progress without moving HEAD, and its tree hash
// equals the tree of the commit later made from the same files.
func TestSnapshotMatchesLaterCommitTree(t *testing.T) {
	ctx := context.Background()
	m := Manager{MirrorsDir: t.TempDir(), RunsDir: t.TempDir(), AuthorName: "aegis", AuthorMail: "aegis@local"}
	mirror, _ := m.EnsureMirror(ctx, newRemote(t))
	base, _ := m.ResolveRef(ctx, mirror, "main")
	wt, _ := m.CreateWorktree(ctx, mirror, "w1", base)
	os.WriteFile(filepath.Join(wt.Path, "app", "a.py"), []byte("x = 3\n"), 0o644)
	snap, tree, err := m.Snapshot(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if head, _ := m.Head(ctx, wt); head != base {
		t.Fatal("Snapshot moved HEAD")
	}
	if changes, _ := m.Diff(ctx, wt, base, snap); len(changes) != 1 {
		t.Fatalf("snapshot diff = %+v", changes)
	}
	sha, _ := m.CommitWorking(ctx, wt, "agent change", "k1")
	if got, _ := m.TreeOf(ctx, wt, sha); got != tree {
		t.Fatalf("commit tree %s != snapshot tree %s", got, tree)
	}
}
