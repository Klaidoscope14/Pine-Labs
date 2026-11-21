// Package sandbox builds the hardened container settings every gate run uses.
package sandbox

import (
	"fmt"
	"strconv"

	"aegis/internal/config"
)

// HardeningArgs returns the `docker run` flags that lock a sandbox down.
// All limits come from config; the security flags are fixed and non-optional.
func HardeningArgs(s config.Sandbox) ([]string, error) {
	if s.Network != "none" {
		return nil, fmt.Errorf("sandbox network must be none, got %q", s.Network)
	}
	return []string{
		"--network=none",
		"--read-only",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--user=10001:10001",
		"--ipc=none",
		"--pids-limit=" + strconv.Itoa(s.PIDs),
		"--memory=" + strconv.Itoa(s.MemoryMB) + "m",
		"--memory-swap=" + strconv.Itoa(s.MemoryMB) + "m",
		"--cpus=" + strconv.FormatFloat(s.CPUs, 'f', -1, 64),
		"--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=256m",
		"--env=HOME=/tmp",
	}, nil
}
