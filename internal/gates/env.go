package gates

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aegis/internal/config"
	"aegis/internal/sandbox"
	"aegis/internal/workspace"
)

// Env is everything a gate needs for one evaluation of one commit.
type Env struct {
	Sandbox   sandbox.Provider
	Image     string
	Timeout   time.Duration
	MaxOutput int64

	Repo    config.Repo
	HeadDir string // checked-out head commit (the run worktree)
	BaseDir string // exported tree of the base commit (shared, read-only)
	BaseSHA string
	HeadSHA string

	// OutRoot is a per-evaluation scratch directory; each gate gets a subdirectory.
	OutRoot string
	// BaseCacheDir caches base-commit results so concurrent runs on the same base share them.
	BaseCacheDir string

	RulesDir  string
	HiddenDir string
	ReplayDir string

	Changes []workspace.Change

	// From the Architect's plan.
	Planned, Adjacent        []string
	Protected                []string
	BudgetFiles, BudgetLines int
	InterfaceChangesDeclared bool
}

type sandboxMount = sandbox.Mount

// ErrInfra marks failures of the gate machinery itself (Docker down, disk full).
// The workflow retries these; they are never turned into a pass.
var ErrInfra = errors.New("gate infrastructure error")

func (e Env) outDir(gate string) (string, error) {
	dir := filepath.Join(e.OutRoot, gate)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInfra, err)
	}
	// The sandbox user (uid 10001) must be able to write reports here.
	if err := os.Chmod(dir, 0o777); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInfra, err)
	}
	return dir, nil
}

// run executes a tool in the sandbox with the given tree mounted read-only at /work.
func (e Env) run(ctx context.Context, tree, out string, cmd []string, extra []sandbox.Mount, env map[string]string) (sandbox.Result, error) {
	mounts := []sandbox.Mount{{HostPath: tree, ContainerPath: "/work", ReadOnly: true}}
	if out != "" {
		mounts = append(mounts, sandbox.Mount{HostPath: out, ContainerPath: "/out"})
	}
	mounts = append(mounts, extra...)
	res, err := e.Sandbox.Run(ctx, sandbox.Spec{Image: e.Image, Cmd: cmd, Workdir: "/work",
		Mounts: mounts, Env: env, Timeout: e.Timeout})
	if err != nil {
		return res, fmt.Errorf("%w: %v", ErrInfra, err)
	}
	return res, nil
}

// readReport reads a tool report with a size cap and decodes it.
func readReport(path string, max int64, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > max {
		return fmt.Errorf("report %s exceeds %d bytes", filepath.Base(path), max)
	}
	return json.Unmarshal(data, v)
}

// workRel turns a container path (/work/app/x.py) into a repo-relative one.
func workRel(p string) string {
	p = strings.TrimPrefix(p, "/work/")
	p = strings.TrimPrefix(p, "./")
	return filepath.ToSlash(p)
}

// snippet reads lines [from, to] of a repo file for fingerprinting.
func snippet(root, rel string, from, to int) string {
	if to < from {
		to = from
	}
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	defer f.Close()
	var b strings.Builder
	s := bufio.NewScanner(f)
	for n := 1; s.Scan(); n++ {
		if n >= from && n <= to {
			b.WriteString(s.Text())
			b.WriteByte('\n')
		}
		if n > to {
			break
		}
	}
	return b.String()
}

func tail(b []byte, n int) string {
	b = bytes.TrimSpace(b)
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}

// cached loads base-commit findings from the cache or computes and stores them.
func (e Env) cached(key string, compute func() ([]Finding, error)) ([]Finding, error) {
	if e.BaseCacheDir == "" {
		return compute()
	}
	path := filepath.Join(e.BaseCacheDir, key+".json")
	if data, err := os.ReadFile(path); err == nil {
		var fs []Finding
		if json.Unmarshal(data, &fs) == nil {
			return fs, nil
		}
	}
	fs, err := compute()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(e.BaseCacheDir, 0o750); err == nil {
		if data, err := json.Marshal(fs); err == nil {
			tmp := path + ".tmp-" + fmt.Sprint(time.Now().UnixNano())
			if os.WriteFile(tmp, data, 0o640) == nil {
				_ = os.Rename(tmp, path)
			}
		}
	}
	return fs, nil
}
