// Package agents defines the contract every agent implementation satisfies.
// Phase 1 uses scenario-driven stubs; Phase 2 adds Claude-backed agents behind
// the same interface. Outputs are always validated against the JSON Schemas by
// the caller, never trusted.
package agents

import (
	"context"
	"encoding/json"
	"errors"
)

// Run identifies the run an agent call belongs to (ledger, budgets, replay).
type Run struct {
	TenantID string
	RunID    string
	Scenario string
	Repo     string
	Attempt  int
}

// AnalyzeInput is the Analyst's input.
type AnalyzeInput struct {
	Run
	Requirement string
	Worktree    string // read-only for the Analyst
}

// ArchitectInput is the Architect's input.
type ArchitectInput struct {
	Run
	RequirementSpec json.RawMessage
	Worktree        string // read-only for the Architect
	// Conflict is a Developer design conflict from the previous plan (F1
	// arbitration): the Architect must re-plan around it or keep the plan.
	Conflict string
}

// DevelopInput is the Developer's input.
type DevelopInput struct {
	Run
	RequirementSpec json.RawMessage
	Plan            json.RawMessage
	Worktree        string
	Diagnostics     string // what failed last time, already redacted
	// BaseSHA is the commit the change is measured against (diff budget).
	BaseSHA string
	// BudgetFiles and BudgetLines are the effective diff budget the scope gate enforces.
	BudgetFiles, BudgetLines int
	// SelfCheck, if set, runs the real scope, lint and tests gates on the work in
	// progress and returns their failures ("" when clean). In-process only.
	SelfCheck func(ctx context.Context) string `json:"-"`
}

// DevelopOutput is a proposed change, or a design conflict.
type DevelopOutput struct {
	// Patch is a unified diff against the base (stub agents).
	Patch []byte
	// EditedInPlace means the agent changed the worktree directly (LLM agents);
	// the caller commits the working tree.
	EditedInPlace bool
	// Conflict is a developer_result JSON with status DESIGN_CONFLICT, if any.
	Conflict json.RawMessage
	Notes    []Decision
}

// Decision is one logged judgement call.
type Decision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// SummaryInput is what the Release writer sees.
type SummaryInput struct {
	Run
	RequirementSpec json.RawMessage
	Plan            json.RawMessage
	GateSummaries   []string
	DiffStat        string
}

// Agents is implemented by stub and LLM agents alike.
type Agents interface {
	Analyze(ctx context.Context, in AnalyzeInput) (json.RawMessage, error)
	Architect(ctx context.Context, in ArchitectInput) (json.RawMessage, error)
	Develop(ctx context.Context, in DevelopInput) (DevelopOutput, error)
}

// Summarizer is optionally implemented by agents that write the human summary.
type Summarizer interface {
	Summarize(ctx context.Context, in SummaryInput) (summary string, risks []string, err error)
}

// ErrNeedsHuman means the agent's output requires a human decision before the
// run can continue (e.g. a plan that touches a protected path).
var ErrNeedsHuman = errors.New("needs a human decision")
