package ledger

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/jackc/pgx/v5"
)

// RecordEvent appends an audit event (e.g. a refused approval) to the tenant's chain.
func (s *Store) RecordEvent(ctx context.Context, tenant, runID, kind string, detail map[string]any) error {
	return s.tx(ctx, tenant, func(tx pgx.Tx) error {
		return appendAudit(ctx, tx, tenant, runID, kind, detail)
	})
}

// SetRunMeta records who submitted a run and its risk tier (set once each).
func (s *Store) SetRunMeta(ctx context.Context, tenant, runID, submitter, riskTier string) error {
	return s.tx(ctx, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET
			submitter = CASE WHEN submitter = '' THEN $2 ELSE submitter END,
			risk_tier = CASE WHEN $3 <> '' THEN $3 ELSE risk_tier END
			WHERE run_id = $1`, runID, submitter, riskTier)
		return err
	})
}

// PathsOverlap reports whether two path patterns (plain paths or ** globs) can
// name the same file. It errs on the side of overlap.
func PathsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	if ok, _ := doublestar.Match(a, b); ok {
		return true
	}
	if ok, _ := doublestar.Match(b, a); ok {
		return true
	}
	// Two globs: compare their literal prefixes up to the first wildcard.
	pa, pb := literalPrefix(a), literalPrefix(b)
	if (pa != a || pb != b) && (hasPrefix(pa, pb) || hasPrefix(pb, pa)) {
		return true
	}
	return false
}

func literalPrefix(p string) string {
	for i, r := range p {
		if r == '*' || r == '?' || r == '[' || r == '{' {
			return p[:i]
		}
	}
	return p
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// LeaseConflict names the run holding an overlapping path.
type LeaseConflict struct {
	HolderRun string `json:"holder_run"`
	Path      string `json:"path"`
	Expires   time.Time
}

// AcquireLeases claims paths for runID on repo until now+ttl. If another live run
// holds an overlapping path it returns that conflict and claims nothing. Calling
// it again for the same run renews the lease. Serialised per (tenant, repo).
func (s *Store) AcquireLeases(ctx context.Context, tenant, repo, runID string, paths []string, ttl time.Duration) (*LeaseConflict, error) {
	var conflict *LeaseConflict
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "lease:"+tenant+":"+repo); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM scope_leases WHERE repo = $1 AND expires_at < now()`, repo); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT run_id, path, expires_at FROM scope_leases WHERE repo = $1 AND run_id <> $2 ORDER BY created_at`, repo, runID)
		if err != nil {
			return err
		}
		type held struct {
			run, path string
			exp       time.Time
		}
		var others []held
		for rows.Next() {
			var h held
			if err := rows.Scan(&h.run, &h.path, &h.exp); err != nil {
				rows.Close()
				return err
			}
			others = append(others, h)
		}
		rows.Close()
		for _, p := range paths {
			for _, h := range others {
				if PathsOverlap(p, h.path) {
					conflict = &LeaseConflict{HolderRun: h.run, Path: p, Expires: h.exp}
					return nil
				}
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM scope_leases WHERE repo = $1 AND run_id = $2`, repo, runID); err != nil {
			return err
		}
		uniq := append([]string(nil), paths...)
		sort.Strings(uniq)
		last := ""
		for _, p := range uniq {
			if p == last {
				continue
			}
			last = p
			if _, err := tx.Exec(ctx, `INSERT INTO scope_leases (tenant_id, repo, run_id, path, expires_at)
				VALUES ($1,$2,$3,$4, now() + $5::interval)`, tenant, repo, runID, p, fmt.Sprintf("%d seconds", int(ttl.Seconds()))); err != nil {
				return err
			}
		}
		return appendAudit(ctx, tx, tenant, runID, "scope.lease_acquired", map[string]any{"repo": repo, "paths": uniq, "ttl_seconds": int(ttl.Seconds())})
	})
	return conflict, err
}

// ReleaseLeases drops every lease the run holds.
func (s *Store) ReleaseLeases(ctx context.Context, tenant, runID string) error {
	return s.tx(ctx, tenant, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM scope_leases WHERE run_id = $1`, runID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return appendAudit(ctx, tx, tenant, runID, "scope.lease_released", map[string]any{"paths": tag.RowsAffected()})
	})
}

// CountAwaitingReview counts the tenant's runs waiting for a human, except runID.
func (s *Store) CountAwaitingReview(ctx context.Context, tenant, exceptRun string) (int, error) {
	var n int
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM runs WHERE status = 'AWAITING_HUMAN' AND run_id <> $1`, exceptRun).Scan(&n)
	})
	return n, err
}

// ReconcileRun sets the final status of a run whose workflow has already closed
// (e.g. cancelled while no worker served its queue), with an audit event.
func (s *Store) ReconcileRun(ctx context.Context, tenant, runID, status, reason string) error {
	return s.tx(ctx, tenant, func(tx pgx.Tx) error {
		var old string
		if err := tx.QueryRow(ctx, `SELECT status FROM runs WHERE run_id = $1 FOR UPDATE`, runID).Scan(&old); err != nil {
			return err
		}
		if old == status {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = $2, updated_at = now() WHERE run_id = $1`, runID, status); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM scope_leases WHERE run_id = $1`, runID); err != nil {
			return err
		}
		return appendAudit(ctx, tx, tenant, runID, "run.reconciled", map[string]any{"from": old, "to": status, "reason": reason})
	})
}

// RecordMerge sets a run's merge outcome (MERGED or MERGE_FAILED) with an audit event.
func (s *Store) RecordMerge(ctx context.Context, tenant, runID, status, mergedSHA, reason string) error {
	return s.tx(ctx, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = $2,
			head_sha = CASE WHEN $3 <> '' THEN $3 ELSE head_sha END, updated_at = now() WHERE run_id = $1`, runID, status, mergedSHA); err != nil {
			return err
		}
		return appendAudit(ctx, tx, tenant, runID, "run.merge", map[string]any{"status": status, "merged_sha": mergedSHA, "reason": reason})
	})
}
