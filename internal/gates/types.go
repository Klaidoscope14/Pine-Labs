// Package gates runs deterministic pass/fail checks and normalises their output.
//
// Tool output from the sandbox is untrusted: gates read only machine-readable
// reports, decode them strictly with a size cap, and cross-check exit codes.
package gates

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Verdict is ordered from best to worst.
type Verdict string

const (
	Pass           Verdict = "PASS"
	Flag           Verdict = "FLAG"
	RemediableFail Verdict = "REMEDIABLE_FAIL"
	HardBlock      Verdict = "HARD_BLOCK"
)

var rank = map[Verdict]int{Pass: 0, Flag: 1, RemediableFail: 2, HardBlock: 3}

// Worse returns the more severe of two verdicts.
func Worse(a, b Verdict) Verdict {
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Finding is one issue, anchored to a file and line where possible.
type Finding struct {
	RuleID      string  `json:"rule_id"`
	Severity    string  `json:"severity"` // LOW, MEDIUM, HIGH, CRITICAL
	File        string  `json:"file,omitempty"`
	Line        int     `json:"line,omitempty"`
	Message     string  `json:"message"`
	Confidence  string  `json:"confidence,omitempty"`
	Verdict     Verdict `json:"verdict"`
	Fingerprint string  `json:"fingerprint"`
	// PreExisting is true when the same finding exists on the base commit.
	PreExisting bool `json:"pre_existing,omitempty"`
}

// Fingerprint identifies a finding independent of line numbers, so it can be
// matched across commits (OPEN on attempt 1, RESOLVED on attempt 2).
func Fingerprint(gate, rule, file, snippet string) string {
	norm := strings.Join(strings.Fields(snippet), " ")
	sum := sha256.Sum256([]byte(gate + "\x00" + rule + "\x00" + file + "\x00" + norm))
	return hex.EncodeToString(sum[:12])
}

// Result is the normalised outcome of one gate.
type Result struct {
	Gate        string             `json:"gate"`
	Verdict     Verdict            `json:"verdict"`
	Findings    []Finding          `json:"findings"`
	Resolved    []Finding          `json:"resolved,omitempty"`
	Metrics     map[string]float64 `json:"metrics,omitempty"`
	Summary     string             `json:"summary"`
	ArtifactRef string             `json:"artifact_ref,omitempty"`
	ToolVersion string             `json:"tool_version,omitempty"`
	DurationMS  int64              `json:"duration_ms"`
}

// Passed is true unless the gate failed or blocked.
func (r Result) Passed() bool { return r.Verdict == Pass || r.Verdict == Flag }

// Finalize sets the verdict to the worst of its new (non pre-existing) findings,
// sorted for stable output.
func (r *Result) Finalize() {
	if r.Verdict == "" {
		r.Verdict = Pass
	}
	for _, f := range r.Findings {
		if !f.PreExisting {
			r.Verdict = Worse(r.Verdict, f.Verdict)
		}
	}
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	if r.Summary == "" {
		n := 0
		for _, f := range r.Findings {
			if !f.PreExisting {
				n++
			}
		}
		r.Summary = fmt.Sprintf("%d new finding(s)", n)
	}
}

// MarkPreExisting flags head findings that also appear on the base commit.
func MarkPreExisting(head []Finding, base []Finding) []Finding {
	seen := map[string]bool{}
	for _, f := range base {
		seen[f.Fingerprint] = true
	}
	for i := range head {
		if seen[head[i].Fingerprint] {
			head[i].PreExisting = true
		}
	}
	return head
}

// Resolved lists base findings that no longer appear on head.
func Resolved(head []Finding, base []Finding) []Finding {
	now := map[string]bool{}
	for _, f := range head {
		now[f.Fingerprint] = true
	}
	var out []Finding
	for _, f := range base {
		if !now[f.Fingerprint] {
			out = append(out, f)
		}
	}
	return out
}
