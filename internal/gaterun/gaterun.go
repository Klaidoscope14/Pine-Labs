// Package gaterun runs a tier of gates against one commit and combines the results.
package gaterun

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"aegis/internal/gates"
	"aegis/internal/scope"
)

type gateFunc func(context.Context, gates.Env) (gates.Result, error)

var registry = map[string]gateFunc{
	"scope": func(_ context.Context, e gates.Env) (gates.Result, error) {
		return scope.Check(e.Changes, scope.Plan{Planned: e.Planned, Adjacent: e.Adjacent}, e.Protected,
			scope.Budget{Files: e.BudgetFiles, Lines: e.BudgetLines}), nil
	},
	"lint":         gates.Lint,
	"tests":        gates.Tests,
	"hidden_tests": gates.HiddenTests,
	"semgrep":      gates.Semgrep,
	"bandit":       gates.Bandit,
	"secrets":      gates.Secrets,
	"dependencies": gates.Dependencies,
	"openapi":      gates.OpenAPI,
	"replay":       gates.Replay,
}

// Known reports whether a gate name is implemented.
func Known(name string) bool { _, ok := registry[name]; return ok }

// Outcome is the combined result of a tier.
type Outcome struct {
	Verdict gates.Verdict  `json:"verdict"`
	Results []gates.Result `json:"results"`
}

// Run executes the named gates with bounded parallelism, in the configured order.
// An infrastructure error in any gate fails the whole tier (never a silent pass).
func Run(ctx context.Context, env gates.Env, names []string, parallelism int) (Outcome, error) {
	if parallelism < 1 {
		parallelism = 1
	}
	results := make([]gates.Result, len(names))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(parallelism)
	var mu sync.Mutex
	for i, name := range names {
		fn, ok := registry[name]
		if !ok {
			return Outcome{}, fmt.Errorf("unknown gate %q", name)
		}
		g.Go(func() error {
			start := time.Now()
			r, err := fn(gctx, env)
			if err != nil {
				return fmt.Errorf("gate %s: %w", name, err)
			}
			r.Gate = name
			r.DurationMS = time.Since(start).Milliseconds()
			mu.Lock()
			results[i] = r
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return Outcome{}, err
	}
	out := Outcome{Verdict: gates.Pass, Results: results}
	for _, r := range results {
		out.Verdict = gates.Worse(out.Verdict, r.Verdict)
	}
	return out, nil
}
