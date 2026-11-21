package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCappedWriter(t *testing.T) {
	c := &capped{max: 5}
	_, _ = c.Write([]byte("abc"))
	_, _ = c.Write([]byte("defgh"))
	if string(c.Bytes()) != "abcde" || !c.truncated {
		t.Fatalf("got %q truncated=%v", c.Bytes(), c.truncated)
	}
}

func TestRunRejectsBadSpecs(t *testing.T) {
	d := Docker{Policy: Policy{CPUs: 1, MemoryMB: 256, PIDs: 64}}
	cases := []Spec{
		{Cmd: []string{"true"}, Timeout: time.Second},
		{Image: "x", Timeout: time.Second},
		{Image: "x", Cmd: []string{"true"}},
		{Image: "x", Cmd: []string{"true"}, Timeout: time.Second, Mounts: []Mount{{HostPath: "rel", ContainerPath: "/w"}}},
		{Image: "x", Cmd: []string{"true"}, Timeout: time.Second, Env: map[string]string{"bad key": "v"}},
	}
	for i, s := range cases {
		if _, err := d.Run(context.Background(), s); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

// Integration: needs Docker and the sandbox image. Skipped otherwise.
func TestDockerSandboxIsLockedDown(t *testing.T) {
	image := os.Getenv("AEGIS_SANDBOX_IMAGE")
	if image == "" {
		t.Skip("AEGIS_SANDBOX_IMAGE not set")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	d := Docker{Policy: Policy{CPUs: 1, MemoryMB: 256, PIDs: 64}, MaxOutputBytes: 1 << 16}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	res, err := d.Run(ctx, Spec{Image: image, Cmd: []string{"cat", "/w/f.txt"}, Timeout: 30 * time.Second,
		Mounts: []Mount{{HostPath: dir, ContainerPath: "/w", ReadOnly: true}}})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "hi" {
		t.Fatalf("read mount: %v %+v %q", err, res, res.Stderr)
	}
	res, _ = d.Run(ctx, Spec{Image: image, Cmd: []string{"touch", "/w/new"}, Timeout: 30 * time.Second,
		Mounts: []Mount{{HostPath: dir, ContainerPath: "/w", ReadOnly: true}}})
	if res.ExitCode == 0 {
		t.Error("write to a read-only mount succeeded")
	}
	res, _ = d.Run(ctx, Spec{Image: image, Cmd: []string{"python", "-c",
		"import socket; socket.create_connection(('1.1.1.1', 53), timeout=3)"}, Timeout: 30 * time.Second})
	if res.ExitCode == 0 {
		t.Error("sandbox reached the network")
	}
	res, _ = d.Run(ctx, Spec{Image: image, Cmd: []string{"id", "-u"}, Timeout: 30 * time.Second})
	if strings.TrimSpace(string(res.Stdout)) != "10001" {
		t.Errorf("sandbox user = %q, want 10001", res.Stdout)
	}
	res, _ = d.Run(ctx, Spec{Image: image, Cmd: []string{"sleep", "30"}, Timeout: 2 * time.Second})
	if !res.TimedOut {
		t.Error("timeout not detected")
	}
}
