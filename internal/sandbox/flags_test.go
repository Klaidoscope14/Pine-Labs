package sandbox

import (
	"slices"
	"strings"
	"testing"

	"aegis/internal/config"
)

func TestHardeningArgsAreAlwaysPresent(t *testing.T) {
	args, err := HardeningArgs(config.Sandbox{CPUs: 1.5, MemoryMB: 512, PIDs: 128, Network: "none"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--network=none", "--read-only", "--cap-drop=ALL",
		"--security-opt=no-new-privileges", "--pids-limit=128", "--memory=512m", "--cpus=1.5"} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %s in %v", want, args)
		}
	}
}

func TestHardeningArgsRefuseNetwork(t *testing.T) {
	if _, err := HardeningArgs(config.Sandbox{Network: "bridge"}); err == nil {
		t.Fatal("expected refusal for a networked sandbox")
	}
}

func TestRuntimeFlag(t *testing.T) {
	if args := (Policy{CPUs: 1, MemoryMB: 512, PIDs: 64, Runtime: "runsc"}).Args(); args[0] != "--runtime=runsc" {
		t.Fatalf("runtime flag missing: %v", args)
	}
	for _, a := range (Policy{CPUs: 1, MemoryMB: 512, PIDs: 64}).Args() {
		if strings.HasPrefix(a, "--runtime") {
			t.Fatalf("no runtime flag expected: %v", a)
		}
	}
}
