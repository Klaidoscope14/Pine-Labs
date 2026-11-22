package ledger

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// RunRow is one line of the runs list.
type RunRow struct {
	RunID       string    `json:"run_id"`
	Status      string    `json:"status"`
	Requirement string    `json:"requirement"`
	Repo        string    `json:"repo"`
	RiskTier    string    `json:"risk_tier"`
	Submitter   string    `json:"submitter"`
	Attempts    int       `json:"attempts"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	CostUSD     *float64  `json:"cost_usd"`
}

// ListRuns returns the tenant's newest runs, optionally only those in statuses
// (or, with exclude, not in them).
func (s *Store) ListRuns(ctx context.Context, tenant string, statuses []string, exclude bool, limit int) ([]RunRow, error) {
	var out []RunRow
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT r.run_id, r.status, r.requirement, r.repo, r.risk_tier, r.submitter, r.attempts, r.created_at, r.updated_at,
			       (SELECT CASE WHEN count(*) = 0 THEN NULL WHEN bool_and(c.cost_usd IS NOT NULL) THEN SUM(c.cost_usd)::float8 END
			          FROM llm_calls c WHERE c.run_id = r.run_id AND c.error = '')
			FROM runs r
			WHERE cardinality($1::text[]) = 0 OR ((r.status = ANY($1)) <> $2)
			ORDER BY r.created_at DESC LIMIT $3`, statuses, exclude, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r RunRow
			if err := rows.Scan(&r.RunID, &r.Status, &r.Requirement, &r.Repo, &r.RiskTier, &r.Submitter, &r.Attempts,
				&r.CreatedAt, &r.UpdatedAt, &r.CostUSD); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// CountRuns counts the tenant's runs in statuses.
func (s *Store) CountRuns(ctx context.Context, tenant string, statuses []string) (int, error) {
	var n int
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM runs WHERE status = ANY($1)`, statuses).Scan(&n)
	})
	return n, err
}

// RunMeta is the run row plus Phase 3 metadata.
func (s *Store) RunMeta(ctx context.Context, tenant, runID string) (RunRow, error) {
	var r RunRow
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT run_id, status, requirement, repo, risk_tier, submitter, attempts, created_at, updated_at
			FROM runs WHERE run_id = $1`, runID).Scan(&r.RunID, &r.Status, &r.Requirement, &r.Repo, &r.RiskTier, &r.Submitter,
			&r.Attempts, &r.CreatedAt, &r.UpdatedAt)
	})
	return r, err
}

// Approval is one recorded human decision.
type Approval struct {
	Reviewer  string    `json:"reviewer"`
	Decision  string    `json:"decision"`
	HeadSHA   string    `json:"head_sha"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

// Approvals lists the run's accepted decisions, oldest first.
func (s *Store) Approvals(ctx context.Context, tenant, runID string) ([]Approval, error) {
	var out []Approval
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT reviewer, decision, head_sha, note, created_at FROM approvals WHERE run_id = $1 ORDER BY id`, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Approval
			if err := rows.Scan(&a.Reviewer, &a.Decision, &a.HeadSHA, &a.Note, &a.CreatedAt); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// Event is one audit-chain entry for a run.
type Event struct {
	Seq       int64          `json:"seq"`
	Kind      string         `json:"kind"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"created_at"`
}

// RunEvents returns the run's audit events, oldest first.
func (s *Store) RunEvents(ctx context.Context, tenant, runID string) ([]Event, error) {
	var out []Event
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT seq, kind, payload, created_at FROM audit_events WHERE run_id = $1 ORDER BY seq`, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e Event
			var raw string
			if err := rows.Scan(&e.Seq, &e.Kind, &raw, &e.CreatedAt); err != nil {
				return err
			}
			_ = json.Unmarshal([]byte(raw), &e.Payload)
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// LastChange is the newest update time across the tenant's runs (for live views).
func (s *Store) LastChange(ctx context.Context, tenant string) (time.Time, error) {
	var t *time.Time
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT max(updated_at) FROM runs`).Scan(&t)
	})
	if t == nil {
		return time.Time{}, err
	}
	return *t, err
}
