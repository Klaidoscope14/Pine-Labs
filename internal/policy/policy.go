// Package policy evaluates the approval policy written in Rego (PLAN.md §10.8).
// The policy is compiled once; each decision is evaluated in an activity, so the
// Temporal workflow stays deterministic. The policy file's hash goes into the ledger.
package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/open-policy-agent/opa/v1/rego"
)

// Input is everything the policy sees about one decision.
type Input struct {
	Run               Run            `json:"run"`
	Decision          Decision       `json:"decision"`
	Approvals         []string       `json:"approvals"`
	Now               string         `json:"now"`
	FreezeWindows     []FreezeWindow `json:"freeze_windows"`
	RequirePasskey    bool           `json:"require_passkey"`
	RequiredApprovers map[string]int `json:"required_approvers"`
}

type Run struct {
	RiskTier  string `json:"risk_tier"`
	Submitter string `json:"submitter"`
}

type Decision struct {
	Reviewer string   `json:"reviewer"`
	Roles    []string `json:"roles"`
	Approve  bool     `json:"approve"`
	Passkey  bool     `json:"passkey"`
}

type FreezeWindow struct {
	Start  string `json:"start"`
	End    string `json:"end"`
	Reason string `json:"reason"`
}

// Result is the policy's verdict.
type Result struct {
	Accept    bool   `json:"accept"`
	Reason    string `json:"reason"`
	Satisfied bool   `json:"satisfied"`
	Needed    int    `json:"needed"`
	Hash      string `json:"policy_hash"`
}

// Engine is a compiled policy.
type Engine struct {
	query rego.PreparedEvalQuery
	Hash  string
}

// Load compiles every non-test .rego file in dir.
func Load(ctx context.Context, dir string) (*Engine, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.rego"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	h := sha256.New()
	opts := []func(*rego.Rego){rego.Query("data.aegis.approval.result")}
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.rego") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		h.Write(src)
		opts = append(opts, rego.Module(filepath.Base(f), string(src)))
		n++
	}
	if n == 0 {
		return nil, fmt.Errorf("no policy files in %s", dir)
	}
	q, err := rego.New(opts...).PrepareForEval(ctx)
	if err != nil {
		return nil, fmt.Errorf("compile policy: %w", err)
	}
	return &Engine{query: q, Hash: hex.EncodeToString(h.Sum(nil))[:16]}, nil
}

// Decide evaluates one decision. A policy error refuses the decision (fail closed).
func (e *Engine) Decide(ctx context.Context, in Input) (Result, error) {
	if in.Now == "" {
		in.Now = time.Now().UTC().Format(time.RFC3339)
	}
	if in.Approvals == nil {
		in.Approvals = []string{}
	}
	if in.FreezeWindows == nil {
		in.FreezeWindows = []FreezeWindow{}
	}
	if in.Decision.Roles == nil {
		in.Decision.Roles = []string{}
	}
	rs, err := e.query.Eval(ctx, rego.EvalInput(in))
	if err != nil {
		return Result{Hash: e.Hash}, err
	}
	if len(rs) != 1 || len(rs[0].Expressions) != 1 {
		return Result{Hash: e.Hash, Reason: "policy produced no decision"}, nil
	}
	m, ok := rs[0].Expressions[0].Value.(map[string]any)
	if !ok {
		return Result{Hash: e.Hash, Reason: "policy produced no decision"}, nil
	}
	r := Result{Hash: e.Hash}
	r.Accept, _ = m["accept"].(bool)
	r.Satisfied, _ = m["satisfied"].(bool)
	r.Reason, _ = m["reason"].(string)
	switch n := m["needed"].(type) {
	case float64:
		r.Needed = int(n)
	case int:
		r.Needed = n
	default:
		if s := fmt.Sprint(n); s != "" {
			fmt.Sscan(s, &r.Needed)
		}
	}
	return r, nil
}
