// Package workspace manages repo mirrors and per-run git worktrees.
//
// Git is always invoked with an explicit argument list, never through a shell.
// Mirror operations are serialised with a file lock so concurrent runs (even
// across worker processes) can't corrupt a mirror.
package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// Manager owns the mirror and run directories.
type Manager struct {
	MirrorsDir string
	RunsDir    string
	AuthorName string
	AuthorMail string
}

// Worktree is one run's isolated checkout.
type Worktree struct {
	RunID   string `json:"run_id"`
	Mirror  string `json:"mirror"`
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	BaseSHA string `json:"base_sha"`
}

var (
	safeID  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,80}$`)
	shaLike = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func (m Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME="+m.AuthorName, "GIT_AUTHOR_EMAIL="+m.AuthorMail,
		"GIT_COMMITTER_NAME="+m.AuthorName, "GIT_COMMITTER_EMAIL="+m.AuthorMail,
	)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// lock takes an exclusive flock on path+".lock" and returns the unlock func.
func lock(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// MirrorPath is where a remote's bare mirror lives (named by a hash of the remote).
func (m Manager) MirrorPath(remote string) string {
	sum := sha256.Sum256([]byte(remote))
	return filepath.Join(m.MirrorsDir, hex.EncodeToString(sum[:8])+".git")
}

// EnsureMirror clones the remote as a bare mirror, or fetches if it exists.
func (m Manager) EnsureMirror(ctx context.Context, remote string) (string, error) {
	if err := os.MkdirAll(m.MirrorsDir, 0o750); err != nil {
		return "", err
	}
	mirror := m.MirrorPath(remote)
	unlock, err := lock(mirror)
	if err != nil {
		return "", err
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(mirror, "HEAD")); errors.Is(err, os.ErrNotExist) {
		if _, err := m.git(ctx, m.MirrorsDir, "clone", "--bare", "--no-hardlinks", "--", remote, mirror); err != nil {
			return "", err
		}
		return mirror, nil
	}
	_, err = m.git(ctx, mirror, "fetch", "--prune", "origin", "+refs/heads/*:refs/heads/*")
	return mirror, err
}

// ResolveRef returns the full commit SHA for a ref in the mirror.
func (m Manager) ResolveRef(ctx context.Context, mirror, ref string) (string, error) {
	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("invalid ref %q", ref)
	}
	out, err := m.git(ctx, mirror, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if !shaLike.MatchString(sha) {
		return "", fmt.Errorf("unexpected rev-parse output %q", sha)
	}
	return sha, nil
}

// CreateWorktree checks out baseSHA on a new branch agent/run-<id>. Idempotent:
// if the worktree already exists for this run it is returned as-is.
func (m Manager) CreateWorktree(ctx context.Context, mirror, runID, baseSHA string) (Worktree, error) {
	if !safeID.MatchString(runID) || !shaLike.MatchString(baseSHA) {
		return Worktree{}, fmt.Errorf("invalid run id or base sha")
	}
	if err := os.MkdirAll(m.RunsDir, 0o750); err != nil {
		return Worktree{}, err
	}
	wt := Worktree{RunID: runID, Mirror: mirror, Path: filepath.Join(m.RunsDir, runID),
		Branch: "agent/run-" + runID, BaseSHA: baseSHA}
	unlock, err := lock(mirror)
	if err != nil {
		return Worktree{}, err
	}
	defer unlock()
	if _, err := os.Stat(filepath.Join(wt.Path, ".git")); err == nil {
		return wt, nil
	}
	if _, err := m.git(ctx, mirror, "worktree", "add", "-b", wt.Branch, "--", wt.Path, baseSHA); err != nil {
		return Worktree{}, err
	}
	return wt, nil
}

// ApplyAndCommit applies a unified diff on top of HEAD and commits it. See ApplyOnto.
func (m Manager) ApplyAndCommit(ctx context.Context, wt Worktree, patch []byte, message, key string) (string, error) {
	return m.ApplyOnto(ctx, wt, "HEAD", patch, message, key)
}

// ApplyOnto resets the worktree to onto, applies a unified diff and commits it. The commit carries an
// Aegis-Key trailer; if a commit with the same key already exists on the branch
// (a retried activity), its SHA is returned and nothing is applied twice.
func (m Manager) ApplyOnto(ctx context.Context, wt Worktree, onto string, patch []byte, message, key string) (string, error) {
	if sha, ok, err := m.findKey(ctx, wt, key); err != nil || ok {
		return sha, err
	}
	if len(patch) == 0 {
		return "", errors.New("empty patch")
	}
	pf, err := os.CreateTemp("", "aegis-patch-*.diff")
	if err != nil {
		return "", err
	}
	defer os.Remove(pf.Name())
	if _, err := pf.Write(patch); err != nil {
		return "", err
	}
	pf.Close()
	// Undo any half-applied state from a crashed attempt, then apply cleanly.
	if strings.HasPrefix(onto, "-") {
		return "", fmt.Errorf("invalid ref %q", onto)
	}
	if _, err := m.git(ctx, wt.Path, "reset", "--hard", onto); err != nil {
		return "", err
	}
	if _, err := m.git(ctx, wt.Path, "clean", "-fdq"); err != nil {
		return "", err
	}
	if _, err := m.git(ctx, wt.Path, "apply", "--index", "--whitespace=nowarn", "--", pf.Name()); err != nil {
		return "", err
	}
	msg := message + "\n\nAegis-Key: " + key + "\n"
	if _, err := m.git(ctx, wt.Path, "commit", "--no-verify", "--no-gpg-sign", "-q", "-m", msg); err != nil {
		return "", err
	}
	return m.Head(ctx, wt)
}

func (m Manager) findKey(ctx context.Context, wt Worktree, key string) (string, bool, error) {
	out, err := m.git(ctx, wt.Path, "log", "--format=%H%x00%B%x1e", wt.BaseSHA+"..HEAD")
	if err != nil {
		return "", false, err
	}
	for _, rec := range strings.Split(out, "\x1e") {
		sha, body, ok := strings.Cut(strings.TrimSpace(rec), "\x00")
		if !ok {
			continue
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) == "Aegis-Key: "+key {
				return sha, true, nil
			}
		}
	}
	return "", false, nil
}

// Head returns the worktree's current commit.
func (m Manager) Head(ctx context.Context, wt Worktree) (string, error) {
	out, err := m.git(ctx, wt.Path, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// CommitCount counts commits on the run branch since the base.
func (m Manager) CommitCount(ctx context.Context, wt Worktree) (int, error) {
	out, err := m.git(ctx, wt.Path, "rev-list", "--count", wt.BaseSHA+"..HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// Change is one file in a diff.
type Change struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Status  string `json:"status"` // A, M, D, R, C, T
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary"`
	NewMode string `json:"new_mode,omitempty"`
}

// Diff lists changed files between two commits, including renames, deletes and modes.
func (m Manager) Diff(ctx context.Context, wt Worktree, base, head string) ([]Change, error) {
	raw, err := m.git(ctx, wt.Path, "diff", "--raw", "-z", "-M", "--no-abbrev", base, head)
	if err != nil {
		return nil, err
	}
	num, err := m.git(ctx, wt.Path, "diff", "--numstat", "-z", "-M", base, head)
	if err != nil {
		return nil, err
	}
	changes, err := parseRaw(raw)
	if err != nil {
		return nil, err
	}
	stats := parseNumstat(num)
	for i := range changes {
		if s, ok := stats[changes[i].Path]; ok {
			changes[i].Added, changes[i].Deleted, changes[i].Binary = s.added, s.deleted, s.binary
		}
	}
	return changes, nil
}

// DiffText returns the unified diff (for evidence and secret scanning).
func (m Manager) DiffText(ctx context.Context, wt Worktree, base, head string) (string, error) {
	return m.git(ctx, wt.Path, "diff", "-M", base, head)
}

// ShowFile returns a file's content at a commit.
func (m Manager) ShowFile(ctx context.Context, wt Worktree, commit, path string) ([]byte, bool, error) {
	out, err := m.git(ctx, wt.Path, "show", commit+":"+path)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "exists on disk, but not in") {
			return nil, false, nil
		}
		return nil, false, err
	}
	return []byte(out), true, nil
}

// ExportTree writes the tree of a commit into dir (used to scan the base side).
func (m Manager) ExportTree(ctx context.Context, wt Worktree, commit, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	_, err := m.git(ctx, wt.Path, "--work-tree="+dir, "checkout", commit, "--", ".")
	if err != nil {
		return err
	}
	// checkout --work-tree updates the index; restore it to HEAD.
	_, err = m.git(ctx, wt.Path, "reset", "-q")
	return err
}

// RemoveWorktree deletes a run's checkout. The branch is kept as evidence.
func (m Manager) RemoveWorktree(ctx context.Context, wt Worktree) error {
	unlock, err := lock(wt.Mirror)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Stat(wt.Path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	_, err = m.git(ctx, wt.Mirror, "worktree", "remove", "--force", "--", wt.Path)
	return err
}

// parseRaw parses `git diff --raw -z` output.
func parseRaw(out string) ([]Change, error) {
	fields := strings.Split(out, "\x00")
	var changes []Change
	for i := 0; i < len(fields); i++ {
		meta := fields[i]
		if meta == "" {
			continue
		}
		if !strings.HasPrefix(meta, ":") {
			return nil, fmt.Errorf("unexpected raw diff record %q", meta)
		}
		parts := strings.Fields(meta[1:])
		if len(parts) < 5 {
			return nil, fmt.Errorf("short raw diff record %q", meta)
		}
		c := Change{NewMode: parts[1], Status: parts[4][:1]}
		if c.Status == "R" || c.Status == "C" {
			if i+2 >= len(fields) {
				return nil, errors.New("truncated rename record")
			}
			c.OldPath, c.Path = fields[i+1], fields[i+2]
			i += 2
		} else {
			if i+1 >= len(fields) {
				return nil, errors.New("truncated raw record")
			}
			c.Path = fields[i+1]
			i++
		}
		changes = append(changes, c)
	}
	return changes, nil
}

type stat struct {
	added, deleted int
	binary         bool
}

// parseNumstat parses `git diff --numstat -z` output, keyed by new path.
func parseNumstat(out string) map[string]stat {
	res := map[string]stat{}
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		s := stat{}
		if parts[0] == "-" {
			s.binary = true
		} else {
			s.added, _ = strconv.Atoi(parts[0])
			s.deleted, _ = strconv.Atoi(parts[1])
		}
		path := parts[2]
		if path == "" && i+2 < len(fields) { // rename: "", old, new
			path = fields[i+2]
			i += 2
		}
		res[path] = s
	}
	return res
}
