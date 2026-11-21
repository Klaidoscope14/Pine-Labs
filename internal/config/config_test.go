package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		"bad-jurisdiction.yaml":            "not in data_residency.allowed_jurisdictions",
		"bad-untagged.yaml":                "jurisdiction",
		"bad-plaintext-secret.yaml":        "api_key_ref",
		"bad-same-region.yaml":             "dr_region must differ",
		"bad-sandbox-network.yaml":         "network",
		"bad-llm-endpoint-kind.yaml":       "want llm",
		"bad-unknown-fallback.yaml":        "unknown provider",
		"bad-unknown-key.yaml":             "surprise_key",
		"bad-tier-not-enabled.yaml":        "not in gates.enabled",
		"bad-repo-endpoint-kind.yaml":      "want code_host",
		"bad-replay-no-cassettes.yaml":     "needs cassette_dir",
		"bad-bedrock-default-profile.yaml": "explicit aws_profile",
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

func TestResolveSecret(t *testing.T) {
	t.Setenv("AEGIS_TEST_SECRET", "s3cret")
	if v, err := ResolveSecret("env:AEGIS_TEST_SECRET"); err != nil || v != "s3cret" {
		t.Fatalf("got %q, %v", v, err)
	}
	if _, err := ResolveSecret("env:AEGIS_DEFINITELY_UNSET"); !errors.Is(err, ErrSecretUnavailable) {
		t.Errorf("unset env should be unavailable, got %v", err)
	}
	if _, err := ResolveSecret("plain-value"); err == nil {
		t.Error("plain values must be rejected")
	}
}

func TestDSNUsesSecretRef(t *testing.T) {
	t.Setenv("AEGIS_APP_DB_PASSWORD", "p@ss/word")
	cfg, err := LoadFile("testdata/good.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := cfg.DSN("postgres")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "aegis_app:p%40ss%2Fword@") || !strings.Contains(dsn, "sslmode=disable") {
		t.Errorf("unexpected DSN %q", dsn)
	}
}

func TestRepoRemoteResolvesFileURL(t *testing.T) {
	cfg, err := LoadFile("testdata/good.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := cfg.Repo("pine-settlements-svc")
	remote, err := cfg.RepoRemote(r)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(remote) || !strings.HasSuffix(remote, ".data/git/pine-settlements-svc.git") {
		t.Errorf("unexpected remote %q", remote)
	}
}

func TestExtendsMergesAndValidates(t *testing.T) {
	dir := t.TempDir()
	base, _ := os.ReadFile("testdata/good.yaml")
	if err := os.WriteFile(filepath.Join(dir, "base.yaml"), base, 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := "extends: base.yaml\nagents:\n  mode: llm\n"
	if err := os.WriteFile(filepath.Join(dir, "over.yaml"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(filepath.Join(dir, "over.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agents.Mode != "llm" || cfg.Agents.MaxToolCalls != 40 {
		t.Errorf("merge lost values: %+v", cfg.Agents)
	}
	bad := "extends: base.yaml\nsandbox:\n  network: bridge\n"
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(filepath.Join(dir, "bad.yaml")); err == nil {
		t.Error("overlay bypassed the guards")
	}
}

func withNow(t *testing.T, day string) {
	t.Helper()
	d, _ := time.Parse("2006-01-02", day)
	old := now
	now = func() time.Time { return d }
	t.Cleanup(func() { now = old })
}

func TestResidencyExceptions(t *testing.T) {
	base, _ := os.ReadFile("testdata/good.yaml")
	load := func(mutate func(string) string) error {
		p := filepath.Join(t.TempDir(), "c.yaml")
		os.WriteFile(p, []byte(mutate(string(base))), 0o644)
		_, err := LoadFile(p)
		return err
	}
	same := func(s string) string { return s }
	withNow(t, "2026-11-21")
	if err := load(same); err != nil {
		t.Fatalf("valid exception rejected: %v", err)
	}
	cases := map[string]struct {
		mutate func(string) string
		want   string
	}{
		"production": {func(s string) string {
			return strings.Replace(s, "environment: local", "environment: production", 1)
		}, "not allowed in production"},
		"no exception": {func(s string) string {
			return strings.Replace(s, "endpoint: llm-deploy-sf\n", "endpoint: llm-fake\n", 1)
		}, "llm-deploy-sf"},
		"too far ahead": {func(s string) string {
			return strings.Replace(s, `expires: "2026-12-31"`, `expires: "2027-06-30"`, 1)
		}, "more than 90 days"},
		"unknown endpoint": {func(s string) string {
			return strings.Replace(s, "      environments: [local]", "      environments: [local]\n    - endpoint: ghost\n      reason: nothing to see here at all\n      expires: \"2026-12-01\"\n      environments: [local]", 1)
		}, "unknown endpoint"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := load(c.mutate)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want error mentioning %q", err, c.want)
			}
		})
	}
	withNow(t, "2027-01-05")
	if err := load(same); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired exception accepted: %v", err)
	}
}
