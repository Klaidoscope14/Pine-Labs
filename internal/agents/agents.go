// Package agents defines the contract every agent implementation satisfies.
// Phase 1 uses scenario-driven stubs; Phase 2 swaps in LLM agents behind the
// same interface. Outputs are always validated against the JSON Schemas by the
// caller, never trusted.
package agents

import (
	"context"
	"encoding/json"
)

// AnalyzeInput is the Analyst's input.
type AnalyzeInput struct {
	RunID       string
	Scenario    string
	Requirement string
}

// ArchitectInput is the Architect's input.
type ArchitectInput struct {
	RunID           string
	Scenario        string
	RequirementSpec json.RawMessage
	Worktree        string // read-only for the Architect
}

// DevelopInput is the Developer's input.
type DevelopInput struct {
	RunID       string
	Scenario    string
	Attempt     int
	Plan        json.RawMessage
	Worktree    string
	Diagnostics string // what failed last time, already redacted
}

// DevelopOutput is a proposed change, or a design conflict.
type DevelopOutput struct {
	Patch []byte
	// Conflict is a developer_result JSON with status DESIGN_CONFLICT, if any.
	Conflict json.RawMessage
	Notes    []Decision
}

// Decision is one logged judgement call.
type Decision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// Agents is implemented by stub and LLM agents alike.
type Agents interface {
	Analyze(ctx context.Context, in AnalyzeInput) (json.RawMessage, error)
	Architect(ctx context.Context, in ArchitectInput) (json.RawMessage, error)
	Develop(ctx context.Context, in DevelopInput) (DevelopOutput, error)
}
