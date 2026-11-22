package policy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/open-policy-agent/opa/v1/tester"
)

const dir = "../../policies"

// The Rego unit tests (test_* rules in policies/*_test.rego) must all pass.
func TestRegoUnitTests(t *testing.T) {
	results, err := tester.Run(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no Rego tests ran")
	}
	for _, r := range results {
		if r.Fail || r.Error != nil {
			t.Errorf("%s: failed (%v)", r.Name, r.Error)
		}
	}
	t.Logf("%d Rego tests passed", len(results))
}

func TestEngineDecides(t *testing.T) {
	e, err := Load(context.Background(), filepath.Clean(dir))
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Run: Run{RiskTier: "high", Submitter: "priya"},
		Decision:          Decision{Reviewer: "priya", Roles: []string{"reviewer"}, Approve: true},
		RequiredApprovers: map[string]int{"low": 1, "high": 2}}
	r, err := e.Decide(context.Background(), in)
	if err != nil || r.Accept || r.Hash == "" {
		t.Fatalf("submitter approval must be refused: %+v %v", r, err)
	}
	in.Decision.Reviewer = "arjun"
	in.Approvals = []string{"kavya"}
	if r, _ := e.Decide(context.Background(), in); !r.Accept || !r.Satisfied || r.Needed != 2 {
		t.Fatalf("second distinct approval should satisfy: %+v", r)
	}
}
