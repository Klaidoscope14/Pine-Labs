// Package sandbox runs untrusted commands in locked-down containers.
package sandbox

import (
	"fmt"
	"strconv"

	"aegis/internal/config"
)

// Policy is the single source of every container hardening setting.
// Limits come from config; the security flags are fixed and not optional.
type Policy struct {
	CPUs     float64
	MemoryMB int
	PIDs     int
}

// PolicyFromConfig builds a Policy, refusing any networked sandbox.
func PolicyFromConfig(s config.Sandbox) (Policy, error) {
	if s.Network != "none" {
		return Policy{}, fmt.Errorf("sandbox network must be none, got %q", s.Network)
	}
	return Policy{CPUs: s.CPUs, MemoryMB: s.MemoryMB, PIDs: s.PIDs}, nil
}

// Args returns the `docker run` flags for this policy.
func (p Policy) Args() []string {
	return []string{
		"--network=none",
		"--read-only",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--user=10001:10001",
		"--ipc=private",
		"--shm-size=64m",
		"--pids-limit=" + strconv.Itoa(p.PIDs),
		"--memory=" + strconv.Itoa(p.MemoryMB) + "m",
		"--memory-swap=" + strconv.Itoa(p.MemoryMB) + "m",
		"--cpus=" + strconv.FormatFloat(p.CPUs, 'f', -1, 64),
		"--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=256m",
		"--env=HOME=/tmp",
	}
}

// HardeningArgs is kept for the aegis-config CLI.
func HardeningArgs(s config.Sandbox) ([]string, error) {
	p, err := PolicyFromConfig(s)
	if err != nil {
		return nil, err
	}
	return p.Args(), nil
}
