package gates

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aegis/internal/workspace"
)

// integrityEnv writes base and head trees and the change list between them.
func integrityEnv(t *testing.T, base, head map[string]string) Env {
	t.Helper()
	bd, hd := t.TempDir(), t.TempDir()
	write := func(dir string, files map[string]string) {
		for p, s := range files {
			os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755)
			os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
		}
	}
	write(bd, base)
	write(hd, head)
	var changes []workspace.Change
	seen := map[string]bool{}
	for p, s := range head {
		seen[p] = true
		if b, ok := base[p]; !ok {
			changes = append(changes, workspace.Change{Path: p, Status: "A"})
		} else if b != s {
			changes = append(changes, workspace.Change{Path: p, Status: "M"})
		}
	}
	for p := range base {
		if !seen[p] {
			changes = append(changes, workspace.Change{Path: p, Status: "D"})
		}
	}
	return Env{BaseDir: bd, HeadDir: hd, Changes: changes}
}

const baseTest = `import pytest


def test_credit_once():
    assert credit() == 1
    assert balance() == 100


class TestWebhook:
    def test_signature(self):
        assert verify("x")
`

func runIntegrity(t *testing.T, base, head map[string]string) Result {
	t.Helper()
	r, err := Integrity(context.Background(), integrityEnv(t, base, head))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func has(r Result, rule, text string) bool {
	for _, f := range r.Findings {
		if f.RuleID == rule && strings.Contains(f.Message, text) {
			return true
		}
	}
	return false
}

func TestIntegrityAllowsAddingTestsAndCode(t *testing.T) {
	head := baseTest + "\n\ndef test_new_case():\n    assert credit(2) == 2\n"
	r := runIntegrity(t, map[string]string{"tests/test_a.py": baseTest, "app/x.py": "a = 1\n"},
		map[string]string{"tests/test_a.py": head, "app/x.py": "a = 2\n"})
	if r.Verdict != Pass {
		t.Fatalf("adding a test and changing code should pass: %+v", r.Findings)
	}
}

func TestIntegrityBlocksTestTampering(t *testing.T) {
	cases := map[string]struct {
		head map[string]string
		want string
	}{
		"deleted file":   {map[string]string{}, "test file was deleted"},
		"removed test":   {map[string]string{"tests/test_a.py": strings.Replace(baseTest, "def test_credit_once():\n    assert credit() == 1\n    assert balance() == 100\n", "", 1)}, "test_credit_once was removed"},
		"lost assertion": {map[string]string{"tests/test_a.py": strings.Replace(baseTest, "    assert balance() == 100\n", "", 1)}, "lost assertions (2 → 1)"},
		"method removed": {map[string]string{"tests/test_a.py": strings.Replace(baseTest, "        assert verify(\"x\")\n", "        pass\n", 1)}, "TestWebhook.test_signature lost assertions"},
		"skip added":     {map[string]string{"tests/test_a.py": strings.Replace(baseTest, "def test_credit_once", "@pytest.mark.skip(reason=\"flaky\")\ndef test_credit_once", 1)}, "skip or xfail"},
		"xfail added":    {map[string]string{"tests/test_a.py": strings.Replace(baseTest, "def test_credit_once", "@pytest.mark.xfail\ndef test_credit_once", 1)}, "skip or xfail"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := runIntegrity(t, map[string]string{"tests/test_a.py": baseTest}, c.head)
			if r.Verdict != HardBlock || !has(r, "FIN-013", c.want) {
				t.Fatalf("want FIN-013 %q hard block, got %s %+v", c.want, r.Verdict, r.Findings)
			}
		})
	}
}

func TestIntegrityScannerSilencing(t *testing.T) {
	r := runIntegrity(t, map[string]string{"app/x.py": "import os\n"}, map[string]string{"app/x.py": "import os  # noqa: F401\nx = eval(y)  # nosec\n"})
	if r.Verdict != RemediableFail || !has(r, "FIN-014", "suppression") {
		t.Fatalf("new suppression must fail: %s %+v", r.Verdict, r.Findings)
	}
	// An existing suppression that stays is fine.
	r = runIntegrity(t, map[string]string{"app/x.py": "import os  # noqa: F401\n"}, map[string]string{"app/x.py": "import os  # noqa: F401\nx = 1\n"})
	if r.Verdict != Pass {
		t.Fatalf("pre-existing suppression should pass: %+v", r.Findings)
	}
	for _, cfg := range []string{"ruff.toml", "pytest.ini", ".semgrepignore", ".github/workflows/ci.yml"} {
		r = runIntegrity(t, map[string]string{cfg: "a = 1\n"}, map[string]string{cfg: "a = 2\n"})
		if r.Verdict != HardBlock || !has(r, "FIN-014", "configuration changed") {
			t.Fatalf("%s edit must hard block: %+v", cfg, r.Findings)
		}
	}
}

func TestIntegrityPyprojectOnlyToolSections(t *testing.T) {
	base := "[project]\ndependencies = [\"a\"]\n\n[tool.ruff]\nselect = [\"E\", \"S\"]\n"
	deps := strings.Replace(base, `["a"]`, `["a", "b"]`, 1)
	if r := runIntegrity(t, map[string]string{"pyproject.toml": base}, map[string]string{"pyproject.toml": deps}); r.Verdict != Pass {
		t.Fatalf("dependency edits are the dependency gate's job: %+v", r.Findings)
	}
	weaker := strings.Replace(base, `["E", "S"]`, `["E"]`, 1)
	if r := runIntegrity(t, map[string]string{"pyproject.toml": base}, map[string]string{"pyproject.toml": weaker}); r.Verdict != HardBlock {
		t.Fatalf("weakening [tool.ruff] must hard block: %+v", r.Findings)
	}
}
