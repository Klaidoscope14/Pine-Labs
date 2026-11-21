// Package stub replays hand-written scenario outputs in place of LLM agents.
package stub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"aegis/internal/agents"
	"aegis/internal/scenario"
)

// Agents reads outputs from evals/scenarios.
type Agents struct {
	Root string
}

func (a Agents) load(name string) (scenario.Scenario, error) { return scenario.Load(a.Root, name) }

func (a Agents) Analyze(_ context.Context, in agents.AnalyzeInput) (json.RawMessage, error) {
	s, err := a.load(in.Scenario)
	if err != nil {
		return nil, err
	}
	return s.Common(a.Root, "requirement_spec.json")
}

func (a Agents) Architect(_ context.Context, in agents.ArchitectInput) (json.RawMessage, error) {
	s, err := a.load(in.Scenario)
	if err != nil {
		return nil, err
	}
	return s.Common(a.Root, "architecture_plan.json")
}

func (a Agents) Develop(_ context.Context, in agents.DevelopInput) (agents.DevelopOutput, error) {
	s, err := a.load(in.Scenario)
	if err != nil {
		return agents.DevelopOutput{}, err
	}
	if s.DeveloperResult != "" {
		data, err := os.ReadFile(filepath.Join(s.Dir, s.DeveloperResult))
		return agents.DevelopOutput{Conflict: data}, err
	}
	patch, err := s.Patch(in.Attempt)
	if err != nil {
		return agents.DevelopOutput{}, err
	}
	return agents.DevelopOutput{Patch: patch, Notes: []agents.Decision{{
		Decision: "Applied scenario patch " + filepath.Base(s.Attempts[min(in.Attempt, len(s.Attempts))-1]),
		Reason:   "Phase 1 stub developer replays hand-written patches"}}}, nil
}
