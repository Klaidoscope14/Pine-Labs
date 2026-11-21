// Package scope checks a diff against the Architect's plan and the protected paths.
//
// Tiers (PLAN.md §3.1): planned files are allowed; adjacent files are allowed but
// flagged; protected files and symlinks are hard-blocked; anything else is out of
// scope and sent back to the developer. A diff budget caps files and lines.
package scope

import (
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"aegis/internal/gates"
	"aegis/internal/workspace"
)

// Plan is the subset of ArchitecturePlan the checker needs.
type Plan struct {
	Planned  []string `json:"planned"`
	Adjacent []string `json:"adjacent"`
}

// Budget caps the size of a change.
type Budget struct {
	Files int `json:"files"`
	Lines int `json:"lines"`
}

// Tier is where a path landed.
type Tier string

const (
	TierPlanned    Tier = "planned"
	TierAdjacent   Tier = "adjacent"
	TierProtected  Tier = "protected"
	TierOutOfScope Tier = "out_of_scope"
)

func match(patterns []string, path string) bool {
	for _, p := range patterns {
		if ok, err := doublestar.Match(p, path); err == nil && ok {
			return true
		}
	}
	return false
}

// Classify returns a path's tier. Protected wins over everything.
func Classify(path string, plan Plan, protected []string) Tier {
	switch {
	case match(protected, path):
		return TierProtected
	case match(plan.Planned, path):
		return TierPlanned
	case match(plan.Adjacent, path):
		return TierAdjacent
	default:
		return TierOutOfScope
	}
}

// Check evaluates a diff and returns a gate result.
func Check(changes []workspace.Change, plan Plan, protected []string, budget Budget) gates.Result {
	res := gates.Result{Gate: "scope", Metrics: map[string]float64{}}
	lines := 0
	add := func(rule, sev, file, msg string, v gates.Verdict) {
		res.Findings = append(res.Findings, gates.Finding{RuleID: rule, Severity: sev, File: file,
			Message: msg, Verdict: v, Confidence: "HIGH", Fingerprint: gates.Fingerprint("scope", rule, file, msg)})
	}
	for _, c := range changes {
		lines += c.Added + c.Deleted
		paths := []string{c.Path}
		if c.OldPath != "" {
			paths = append(paths, c.OldPath)
		}
		for _, p := range paths {
			if strings.HasPrefix(p, "/") || strings.Contains("/"+p+"/", "/../") || strings.HasPrefix(p, ".git/") {
				add("SCOPE-PATH", "CRITICAL", p, "path escapes the repository", gates.HardBlock)
				continue
			}
			switch Classify(p, plan, protected) {
			case TierProtected:
				add("FIN-005", "CRITICAL", p, "protected file modified without human authorisation", gates.HardBlock)
			case TierAdjacent:
				add("SCOPE-ADJACENT", "LOW", p, "adjacent file changed (allowed, flagged for review)", gates.Flag)
			case TierOutOfScope:
				add("SCOPE-OUT", "HIGH", p, "file is outside the planned scope", gates.RemediableFail)
			}
		}
		if c.NewMode == "120000" {
			add("SCOPE-SYMLINK", "CRITICAL", c.Path, "symlinks may not be added by agents", gates.HardBlock)
		}
		if c.Binary {
			add("SCOPE-BINARY", "HIGH", c.Path, "binary files may not be added by agents", gates.RemediableFail)
		}
	}
	res.Metrics["files"] = float64(len(changes))
	res.Metrics["lines"] = float64(lines)
	if budget.Files > 0 && len(changes) > budget.Files {
		add("SCOPE-BUDGET-FILES", "MEDIUM", "", fmt.Sprintf("%d files changed, budget is %d", len(changes), budget.Files), gates.RemediableFail)
	}
	if budget.Lines > 0 && lines > budget.Lines {
		add("SCOPE-BUDGET-LINES", "MEDIUM", "", fmt.Sprintf("%d lines changed, budget is %d", lines, budget.Lines), gates.RemediableFail)
	}
	if len(changes) == 0 {
		add("SCOPE-EMPTY", "MEDIUM", "", "the change is empty", gates.RemediableFail)
	}
	res.Finalize()
	return res
}
