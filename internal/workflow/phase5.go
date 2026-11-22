package workflow

import (
	"context"
	"slices"
	"time"

	"go.temporal.io/sdk/temporal"

	"aegis/internal/policy"
)

// ---- Phase 5 (PLAN.md §10.8) ----

type PolicyInput struct {
	Run       Input     `json:"run"`
	Decision  Decision  `json:"decision"`
	RiskTier  string    `json:"risk_tier"`
	Approvals []string  `json:"approvals"`
	Now       time.Time `json:"now"`
}

type PolicyResult struct {
	Accept    bool   `json:"accept"`
	Reason    string `json:"reason"`
	Satisfied bool   `json:"satisfied"`
	Needed    int    `json:"needed"`
	Hash      string `json:"policy_hash"`
}

// reviewerRoles returns the configured roles of a reviewer. Outside production, an
// identity that isn't a configured user (the CLI scenario runner's simulated
// reviewers) is treated as a reviewer; in production it has no roles.
func (a *Activities) reviewerRoles(id string) []string {
	for _, u := range a.Cfg.API.Users {
		if u.ID == id {
			return u.Roles
		}
	}
	if a.Cfg.Deployment.Environment != "production" {
		return []string{"reviewer"}
	}
	return nil
}

// DecidePolicy evaluates one human decision against the Rego approval policy and
// records the verdict (with the policy's hash) in the audit chain. A policy
// error is retried, never turned into an accept.
func (a *Activities) DecidePolicy(ctx context.Context, in PolicyInput) (PolicyResult, error) {
	defer keepAlive(ctx)()
	if a.Policy == nil {
		return PolicyResult{}, temporal.NewNonRetryableApplicationError("approval policy not loaded (approval.policy_dir)", ErrScenario, nil)
	}
	var freezes []policy.FreezeWindow
	for _, f := range in.Run.Settings.FreezeWindows {
		freezes = append(freezes, policy.FreezeWindow{Start: f.Start.UTC().Format(time.RFC3339), End: f.End.UTC().Format(time.RFC3339), Reason: f.Reason})
	}
	r, err := a.Policy.Decide(ctx, policy.Input{
		Run:               policy.Run{RiskTier: in.RiskTier, Submitter: in.Run.Submitter},
		Decision:          policy.Decision{Reviewer: in.Decision.Reviewer, Roles: slices.Clone(a.reviewerRoles(in.Decision.Reviewer)), Approve: in.Decision.Approve, Passkey: in.Decision.Passkey},
		Approvals:         in.Approvals,
		Now:               in.Now.UTC().Format(time.RFC3339),
		FreezeWindows:     freezes,
		RequirePasskey:    a.Cfg.Approval.RequirePasskey,
		RequiredApprovers: in.Run.Settings.RequiredApprovers,
	})
	if err != nil {
		return PolicyResult{}, err
	}
	_ = a.Store.RecordEvent(ctx, in.Run.TenantID, in.Run.RunID, "policy.decided", map[string]any{
		"reviewer": in.Decision.Reviewer, "approve": in.Decision.Approve, "passkey": in.Decision.Passkey,
		"accept": r.Accept, "reason": r.Reason, "satisfied": r.Satisfied, "needed": r.Needed, "policy_hash": r.Hash})
	return PolicyResult{Accept: r.Accept, Reason: r.Reason, Satisfied: r.Satisfied, Needed: r.Needed, Hash: r.Hash}, nil
}
