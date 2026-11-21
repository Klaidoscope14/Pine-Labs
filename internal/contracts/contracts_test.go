package contracts

import (
	"os"
	"path/filepath"
	"testing"

	"aegis/schemas"
)

func TestExamplesMatchContracts(t *testing.T) {
	for name := range schemas.AgentSchemas {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "agents", "examples", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(name, raw); err != nil {
				t.Errorf("example does not match its contract: %v", err)
			}
		})
	}
}

func TestMalformedOutputsAreRejected(t *testing.T) {
	cases := map[string]struct{ contract, doc string }{
		"not json":                {"requirement_spec", `{"clarified_summary": `},
		"missing field":           {"requirement_spec", `{"clarified_summary":"x"}`},
		"extra field":             {"security_report", `{"commit_sha":"abcdef1","verdict":"PASS","findings":[],"note":"x"}`},
		"bad enum":                {"security_report", `{"commit_sha":"abcdef1","verdict":"MAYBE","findings":[]}`},
		"conflict without detail": {"developer_result", `{"status":"DESIGN_CONFLICT","modified_files":[],"decision_log":[]}`},
		"implemented without sha": {"developer_result", `{"status":"IMPLEMENTED","modified_files":["a.py"],"decision_log":[]}`},
		"release auto-approved":   {"release_packet", `{"approval":"APPROVED"}`},
		"unknown contract":        {"merge_request", `{}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Validate(c.contract, []byte(c.doc)); err == nil {
				t.Error("malformed output was accepted")
			}
		})
	}
}
