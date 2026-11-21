package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Mount binds a host path into the container.
type Mount struct {
	HostPath      string
	ContainerPath string
	ReadOnly      bool
}

// Spec describes one sandboxed command.
type Spec struct {
	Image   string
	Cmd     []string
	Workdir string
	Mounts  []Mount
	Env     map[string]string
	Timeout time.Duration
}

// Result is what came back. Stdout/Stderr are untrusted and capped.
type Result struct {
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	Truncated bool
	TimedOut  bool
	OOMKilled bool
	Duration  time.Duration
}

// Provider runs sandboxed commands. The workflow only ever sees this interface,
// so the Docker implementation can be swapped for Kubernetes + gVisor in production.
type Provider interface {
	Run(ctx context.Context, spec Spec) (Result, error)
}

// Docker runs sandboxes with the docker CLI, using argument lists only (no shell).
type Docker struct {
	Policy         Policy
	Binary         string // usually "docker"
	MaxOutputBytes int64
}

var envKey = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// Run starts a fresh container, waits for it, records OOM/timeout, then removes it.
func (d Docker) Run(ctx context.Context, spec Spec) (Result, error) {
	if spec.Image == "" || len(spec.Cmd) == 0 {
		return Result{}, errors.New("sandbox: image and command are required")
	}
	if spec.Timeout <= 0 {
		return Result{}, errors.New("sandbox: timeout is required")
	}
	name := "aegis-sbx-" + randHex(8)
	args := append([]string{"run", "--name", name, "--label", SandboxLabel}, d.Policy.Args()...)
	for _, m := range spec.Mounts {
		if !filepath.IsAbs(m.HostPath) || !filepath.IsAbs(m.ContainerPath) {
			return Result{}, fmt.Errorf("sandbox: mount paths must be absolute: %+v", m)
		}
		if strings.ContainsAny(m.HostPath+m.ContainerPath, ",") {
			return Result{}, fmt.Errorf("sandbox: mount path contains a comma: %+v", m)
		}
		opt := fmt.Sprintf("type=bind,source=%s,target=%s", m.HostPath, m.ContainerPath)
		if m.ReadOnly {
			opt += ",readonly"
		}
		args = append(args, "--mount", opt)
	}
	for k, v := range spec.Env {
		if !envKey.MatchString(k) {
			return Result{}, fmt.Errorf("sandbox: bad env key %q", k)
		}
		args = append(args, "--env", k+"="+v)
	}
	if spec.Workdir != "" {
		args = append(args, "--workdir", spec.Workdir)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Cmd...)

	defer d.remove(name)
	runCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	stdout := &capped{max: d.MaxOutputBytes}
	stderr := &capped{max: d.MaxOutputBytes}
	cmd := exec.CommandContext(runCtx, d.binary(), args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Cancel = func() error { d.kill(name); return nil }
	cmd.WaitDelay = 10 * time.Second

	start := time.Now()
	err := cmd.Run()
	res := Result{
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
		Truncated: stdout.truncated || stderr.truncated,
		Duration:  time.Since(start),
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	case res.TimedOut:
		res.ExitCode = -1
	default:
		return res, fmt.Errorf("sandbox: docker run: %w", err)
	}
	res.OOMKilled = d.oomKilled(name)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, nil
}

// SandboxLabel marks every container this provider starts.
const SandboxLabel = "aegis.sandbox=1"

// Sweep removes stopped sandbox containers left behind by a worker that was
// killed mid-run. It only ever touches containers carrying SandboxLabel.
func (d Docker) Sweep(ctx context.Context) (int, error) {
	out, err := exec.CommandContext(ctx, d.binary(), "ps", "-aq", "--filter", "label="+SandboxLabel,
		"--filter", "status=exited", "--filter", "status=created", "--filter", "status=dead").Output()
	if err != nil {
		return 0, err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return 0, nil
	}
	return len(ids), exec.CommandContext(ctx, d.binary(), append([]string{"rm", "-f"}, ids...)...).Run()
}

func (d Docker) binary() string {
	if d.Binary == "" {
		return "docker"
	}
	return d.Binary
}

func (d Docker) oomKilled(name string) bool {
	out, err := exec.Command(d.binary(), "inspect", "--format", "{{.State.OOMKilled}}", name).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func (d Docker) kill(name string) {
	_ = exec.Command(d.binary(), "kill", name).Run()
}

func (d Docker) remove(name string) {
	_ = exec.Command(d.binary(), "rm", "-f", name).Run()
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// capped is a writer that keeps at most max bytes and records truncation.
type capped struct {
	buf       bytes.Buffer
	max       int64
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := c.max - int64(c.buf.Len())
	if c.max > 0 && room <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if c.max > 0 && int64(len(p)) > room {
		c.buf.Write(p[:room])
		c.truncated = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *capped) Bytes() []byte { return c.buf.Bytes() }
