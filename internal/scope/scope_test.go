package scope

import (
	"testing"

	"aegis/internal/gates"
	"aegis/internal/workspace"
)

var (
	plan      = Plan{Planned: []string{"app/webhooks/handler.py", "app/db.py"}, Adjacent: []string{"tests/**"}}
	protected = []string{"app/config/payout_auth.py", ".agent-rules.md", ".github/**"}
	budget    = Budget{Files: 4, Lines: 100}
)

func ch(path string, added int) workspace.Change {
	return workspace.Change{Path: path, Status: "M", Added: added, NewMode: "100644"}
}

func TestScopeVerdicts(t *testing.T) {
	cases := map[string]struct {
		changes []workspace.Change
		want    gates.Verdict
		rule    string
	}{
		"planned only":          {[]workspace.Change{ch("app/webhooks/handler.py", 10)}, gates.Pass, ""},
		"adjacent flagged":      {[]workspace.Change{ch("app/db.py", 3), ch("tests/test_webhooks.py", 20)}, gates.Flag, "SCOPE-ADJACENT"},
		"out of scope":          {[]workspace.Change{ch("app/payments/service.py", 2)}, gates.RemediableFail, "SCOPE-OUT"},
		"protected blocked":     {[]workspace.Change{ch("app/config/payout_auth.py", 1)}, gates.HardBlock, "FIN-005"},
		"protected glob":        {[]workspace.Change{ch(".github/workflows/ci.yml", 1)}, gates.HardBlock, "FIN-005"},
		"rename from protected": {[]workspace.Change{{Path: "app/db.py", OldPath: "app/config/payout_auth.py", Status: "R"}}, gates.HardBlock, "FIN-005"},
		"symlink blocked":       {[]workspace.Change{{Path: "app/db.py", Status: "A", NewMode: "120000"}}, gates.HardBlock, "SCOPE-SYMLINK"},
		"binary rejected":       {[]workspace.Change{{Path: "app/db.py", Status: "A", Binary: true}}, gates.RemediableFail, "SCOPE-BINARY"},
		"line budget":           {[]workspace.Change{ch("app/db.py", 101)}, gates.RemediableFail, "SCOPE-BUDGET-LINES"},
		"file budget":           {[]workspace.Change{ch("app/db.py", 1), ch("tests/a.py", 1), ch("tests/b.py", 1), ch("tests/c.py", 1), ch("tests/d.py", 1)}, gates.RemediableFail, "SCOPE-BUDGET-FILES"},
		"empty change":          {nil, gates.RemediableFail, "SCOPE-EMPTY"},
		"git dir":               {[]workspace.Change{ch(".git/config", 1)}, gates.HardBlock, "SCOPE-PATH"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := Check(c.changes, plan, protected, budget)
			if r.Verdict != c.want {
				t.Fatalf("verdict = %s, want %s (%+v)", r.Verdict, c.want, r.Findings)
			}
			if c.rule == "" {
				return
			}
			for _, f := range r.Findings {
				if f.RuleID == c.rule {
					return
				}
			}
			t.Errorf("missing finding %s in %+v", c.rule, r.Findings)
		})
	}
}

func TestProtectedBeatsPlanned(t *testing.T) {
	p := Plan{Planned: []string{"app/**"}}
	if Classify("app/config/payout_auth.py", p, protected) != TierProtected {
		t.Error("a planned glob must not unlock a protected file")
	}
}
