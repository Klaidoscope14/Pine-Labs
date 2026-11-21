package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoodConfigLoads(t *testing.T) {
	cfg, err := LoadFile("testdata/good.yaml")
	if err != nil {
		t.Fatalf("good config rejected: %v", err)
	}
	if cfg.Sandbox.Network != "none" {
		t.Errorf("sandbox network = %q, want none", cfg.Sandbox.Network)
	}
	if len(cfg.Hash) != 64 {
		t.Errorf("config hash = %q, want 64 hex chars", cfg.Hash)
	}
	if _, ok := cfg.Endpoint("llm-fake"); !ok {
		t.Error("expected llm-fake endpoint")
	}
}

func TestConfigHashIsStable(t *testing.T) {
	a, err := LoadFile("testdata/good.yaml")
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadFile("testdata/good.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Error("same file produced different hashes")
	}
}

// Each bad fixture must be rejected, and the error must mention why.
func TestBadConfigsAreRejected(t *testing.T) {
	want := map[string]string{
		"bad-jurisdiction.yaml":      "not in data_residency.allowed_jurisdictions",
		"bad-untagged.yaml":          "jurisdiction",
		"bad-plaintext-secret.yaml":  "api_key_ref",
		"bad-same-region.yaml":       "dr_region must differ",
		"bad-sandbox-network.yaml":   "network",
		"bad-llm-endpoint-kind.yaml": "want llm",
		"bad-unknown-fallback.yaml":  "unknown provider",
		"bad-unknown-key.yaml":       "surprise_key",
	}
	files, err := filepath.Glob("testdata/bad-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(want) {
		t.Fatalf("found %d bad fixtures, test expects %d", len(files), len(want))
	}
	for _, f := range files {
		name := filepath.Base(f)
		t.Run(name, func(t *testing.T) {
			_, err := LoadFile(f)
			if err == nil {
				t.Fatal("bad config was accepted")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error is not ErrInvalid: %v", err)
			}
			if !strings.Contains(err.Error(), want[name]) {
				t.Errorf("error %q does not mention %q", err, want[name])
			}
		})
	}
}
