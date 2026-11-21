// Package ledger is the durable record of every run: steps, findings, approvals,
// side effects and a tamper-evident, hash-chained audit log, isolated per tenant
// by Postgres row-level security.
package ledger

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aegis/internal/gates"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate creates or updates the app role and applies pending migrations.
// It runs with the admin (owner) connection; services use the app role.
func Migrate(ctx context.Context, adminDSN, appRole, appPassword string) error {
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	role := pgx.Identifier{appRole}.Sanitize()

	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, appRole).Scan(&exists); err != nil {
		return err
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	// Build the statement server-side so the password is quoted by Postgres itself.
	var stmt string
	if err := conn.QueryRow(ctx, `SELECT format('%s ROLE %s LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE PASSWORD %L', $1::text, $2::text, $3::text)`,
		verb, role, appPassword).Scan(&stmt); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("%s role: %w", strings.ToLower(verb), err)
	}

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `REVOKE ALL ON schema_migrations FROM PUBLIC`); err != nil {
		return err
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var done bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		sql := strings.ReplaceAll(string(body), "{{app_role}}", role)
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Store is the app-role view of the ledger. Every operation is scoped to one tenant.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects with the app role.
func Open(ctx context.Context, appDSN string) (*Store, error) {
	pool, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// tx runs fn in a transaction with aegis.tenant_id set for RLS.
func (s *Store) tx(ctx context.Context, tenant string, fn func(pgx.Tx) error) error {
	if tenant == "" {
		return errors.New("ledger: tenant is required")
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('aegis.tenant_id', $1, true)`, tenant); err != nil {
			return err
		}
		return fn(tx)
	})
}

// Run is one workflow execution.
type Run struct {
	RunID       string `json:"run_id"`
	TenantID    string `json:"tenant_id"`
	Scenario    string `json:"scenario"`
	Repo        string `json:"repo"`
	Requirement string `json:"requirement"`
	BaseSHA     string `json:"base_sha"`
	HeadSHA     string `json:"head_sha"`
	Branch      string `json:"branch"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	ConfigHash  string `json:"config_hash"`
}

// CreateRun inserts a run once; retries are no-ops.
func (s *Store) CreateRun(ctx context.Context, r Run) error {
	return s.tx(ctx, r.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO runs (run_id, tenant_id, scenario, repo, requirement, status, config_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (run_id) DO NOTHING`,
			r.RunID, r.TenantID, r.Scenario, r.Repo, r.Requirement, r.Status, r.ConfigHash)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return appendAudit(ctx, tx, r.TenantID, r.RunID, "run.created", map[string]any{
			"repo": r.Repo, "scenario": r.Scenario, "config_hash": r.ConfigHash})
	})
}

// Transition records step seq of a run (deterministic, from the workflow) and the
// new run status. Replays of the same seq are ignored, so retries never duplicate.
type Transition struct {
	TenantID  string         `json:"tenant_id"`
	RunID     string         `json:"run_id"`
	Seq       int            `json:"seq"`
	Node      string         `json:"node"`
	Attempt   int            `json:"attempt"`
	Status    string         `json:"status"`
	CommitSHA string         `json:"commit_sha"`
	BaseSHA   string         `json:"base_sha,omitempty"`
	Branch    string         `json:"branch,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

func (s *Store) RecordTransition(ctx context.Context, t Transition) error {
	detail, err := json.Marshal(t.Detail)
	if err != nil {
		return err
	}
	if t.Detail == nil {
		detail = []byte("{}")
	}
	return s.tx(ctx, t.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO steps (tenant_id, run_id, seq, node, attempt, status, detail, commit_sha)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (run_id, seq) DO NOTHING`,
			t.TenantID, t.RunID, t.Seq, t.Node, t.Attempt, t.Status, detail, t.CommitSHA)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // already recorded by an earlier attempt
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = $2, attempts = GREATEST(attempts, $3),
			head_sha = CASE WHEN $4 <> '' THEN $4 ELSE head_sha END,
			base_sha = CASE WHEN $5 <> '' THEN $5 ELSE base_sha END,
			branch   = CASE WHEN $6 <> '' THEN $6 ELSE branch END,
			updated_at = now() WHERE run_id = $1`, t.RunID, t.Status, t.Attempt, t.CommitSHA, t.BaseSHA, t.Branch); err != nil {
			return err
		}
		return appendAudit(ctx, tx, t.TenantID, t.RunID, "run.transition", map[string]any{
			"seq": t.Seq, "node": t.Node, "status": t.Status, "attempt": t.Attempt, "commit_sha": t.CommitSHA})
	})
}

// RecordFindings stores one evaluation's findings, anchored to the commit, and
// marks earlier OPEN findings of the gates that ran as RESOLVED when they are gone.
func (s *Store) RecordFindings(ctx context.Context, tenant, runID string, attempt int, commit string, results []gates.Result) error {
	return s.tx(ctx, tenant, func(tx pgx.Tx) error {
		ran := []string{}
		newCount := 0
		for _, r := range results {
			ran = append(ran, r.Gate)
			for _, f := range r.Findings {
				status := "OPEN"
				if f.PreExisting {
					status = "PRE_EXISTING"
				} else {
					newCount++
				}
				if _, err := tx.Exec(ctx, `INSERT INTO findings (tenant_id, run_id, attempt, gate, rule_id, severity, verdict,
					file, line, message, fingerprint, commit_sha, status)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
					ON CONFLICT (run_id, gate, fingerprint, commit_sha) DO NOTHING`,
					tenant, runID, attempt, r.Gate, f.RuleID, f.Severity, string(f.Verdict), f.File, f.Line,
					f.Message, f.Fingerprint, commit, status); err != nil {
					return err
				}
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE findings old SET status = 'RESOLVED'
			WHERE old.run_id = $1 AND old.status = 'OPEN' AND old.commit_sha <> $2 AND old.gate = ANY($3)
			  AND NOT EXISTS (SELECT 1 FROM findings cur WHERE cur.run_id = $1 AND cur.commit_sha = $2
			                  AND cur.gate = old.gate AND cur.fingerprint = old.fingerprint)`, runID, commit, ran)
		if err != nil {
			return err
		}
		return appendAudit(ctx, tx, tenant, runID, "gates.evaluated", map[string]any{
			"commit_sha": commit, "attempt": attempt, "gates": ran, "new_findings": newCount, "resolved": tag.RowsAffected()})
	})
}

// RecordApproval stores a human decision bound to the exact commit and evidence.
func (s *Store) RecordApproval(ctx context.Context, tenant, runID, reviewer, decision, headSHA, evidenceHash, note string) error {
	return s.tx(ctx, tenant, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO approvals (tenant_id, run_id, reviewer, decision, head_sha, evidence_hash, note)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (run_id, reviewer, head_sha) DO NOTHING`,
			tenant, runID, reviewer, decision, headSHA, evidenceHash, note)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return appendAudit(ctx, tx, tenant, runID, "human.decision", map[string]any{
			"reviewer": reviewer, "decision": decision, "head_sha": headSHA, "evidence_hash": evidenceHash})
	})
}

// Once runs fn at most once per key as far as the ledger is concerned: the first
// stored result wins and every later call returns it. fn itself must tolerate a
// rare re-run (e.g. after a crash between fn and the insert).
func (s *Store) Once(ctx context.Context, tenant, runID, key string, fn func() (json.RawMessage, error)) (json.RawMessage, error) {
	var stored json.RawMessage
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT result FROM side_effects WHERE key = $1`, key).Scan(&stored)
	})
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	result, err := fn()
	if err != nil {
		return nil, err
	}
	err = s.tx(ctx, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO side_effects (key, tenant_id, run_id, result) VALUES ($1,$2,$3,$4)
			ON CONFLICT (key) DO NOTHING`, key, tenant, runID, result); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT result FROM side_effects WHERE key = $1`, key).Scan(&stored)
	})
	return stored, err
}

// ---- hash chain ----

func genesis(tenant string) string {
	sum := sha256.Sum256([]byte("aegis-genesis:" + tenant))
	return hex.EncodeToString(sum[:])
}

func chainHash(prev, tenant string, seq int64, runID, kind, payload string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%d\n%s\n%s\n%s", prev, tenant, seq, runID, kind, payload)
	return hex.EncodeToString(h.Sum(nil))
}

// appendAudit adds one event to the tenant's chain. The chain head row is locked
// for the transaction, so concurrent appends are serialised and gap-free.
func appendAudit(ctx context.Context, tx pgx.Tx, tenant, runID, kind string, payload map[string]any) error {
	body, err := json.Marshal(payload) // map keys are sorted: canonical
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO chain_heads (tenant_id, seq, hash) VALUES ($1, 0, $2) ON CONFLICT (tenant_id) DO NOTHING`,
		tenant, genesis(tenant)); err != nil {
		return err
	}
	var seq int64
	var prev string
	if err := tx.QueryRow(ctx, `SELECT seq, hash FROM chain_heads WHERE tenant_id = $1 FOR UPDATE`, tenant).Scan(&seq, &prev); err != nil {
		return err
	}
	seq++
	hash := chainHash(prev, tenant, seq, runID, kind, string(body))
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events (tenant_id, seq, run_id, kind, payload, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, tenant, seq, runID, kind, string(body), prev, hash); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE chain_heads SET seq = $2, hash = $3 WHERE tenant_id = $1`, tenant, seq, hash)
	return err
}

// ErrChainBroken means the audit log was altered.
var ErrChainBroken = errors.New("audit chain broken")

// Verify recomputes the tenant's chain and checks it ends at the recorded head.
func (s *Store) Verify(ctx context.Context, tenant string) (int64, error) {
	var count int64
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT seq, run_id, kind, payload, prev_hash, hash FROM audit_events WHERE tenant_id = $1 ORDER BY seq`, tenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		prev := genesis(tenant)
		var expectSeq int64 = 1
		for rows.Next() {
			var seq int64
			var runID, kind, payload, prevHash, hash string
			if err := rows.Scan(&seq, &runID, &kind, &payload, &prevHash, &hash); err != nil {
				return err
			}
			if seq != expectSeq {
				return fmt.Errorf("%w: expected seq %d, found %d", ErrChainBroken, expectSeq, seq)
			}
			if prevHash != prev || chainHash(prev, tenant, seq, runID, kind, payload) != hash {
				return fmt.Errorf("%w at seq %d", ErrChainBroken, seq)
			}
			prev = hash
			expectSeq++
			count++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		var headSeq int64
		var headHash string
		err = tx.QueryRow(ctx, `SELECT seq, hash FROM chain_heads WHERE tenant_id = $1`, tenant).Scan(&headSeq, &headHash)
		if errors.Is(err, pgx.ErrNoRows) && count == 0 {
			return nil
		}
		if err != nil {
			return err
		}
		if headSeq != count || headHash != prev {
			return fmt.Errorf("%w: head is seq %d, chain ends at %d (events deleted or appended out of band)", ErrChainBroken, headSeq, count)
		}
		return nil
	})
	return count, err
}

// ---- read side ----

// Summary is a run plus its steps and findings, for the CLI.
type Summary struct {
	Run      Run              `json:"run"`
	Steps    []map[string]any `json:"steps"`
	Findings []map[string]any `json:"findings"`
}

func (s *Store) Summary(ctx context.Context, tenant, runID string) (Summary, error) {
	var out Summary
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		r := &out.Run
		if err := tx.QueryRow(ctx, `SELECT run_id, tenant_id, scenario, repo, requirement, base_sha, head_sha, branch, status, attempts, config_hash
			FROM runs WHERE run_id = $1`, runID).Scan(&r.RunID, &r.TenantID, &r.Scenario, &r.Repo, &r.Requirement,
			&r.BaseSHA, &r.HeadSHA, &r.Branch, &r.Status, &r.Attempts, &r.ConfigHash); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT seq, node, attempt, status, commit_sha FROM steps WHERE run_id = $1 ORDER BY seq`, runID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var seq, attempt int
			var node, status, sha string
			if err := rows.Scan(&seq, &node, &attempt, &status, &sha); err != nil {
				return err
			}
			out.Steps = append(out.Steps, map[string]any{"seq": seq, "node": node, "attempt": attempt, "status": status, "commit_sha": sha})
		}
		rows.Close()
		rows, err = tx.Query(ctx, `SELECT gate, rule_id, severity, verdict, file, line, status, attempt, commit_sha FROM findings WHERE run_id = $1 ORDER BY id`, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var gate, rule, sev, verdict, file, status, sha string
			var line, attempt int
			if err := rows.Scan(&gate, &rule, &sev, &verdict, &file, &line, &status, &attempt, &sha); err != nil {
				return err
			}
			out.Findings = append(out.Findings, map[string]any{"gate": gate, "rule_id": rule, "severity": sev, "verdict": verdict,
				"file": file, "line": line, "status": status, "attempt": attempt, "commit_sha": sha})
		}
		return rows.Err()
	})
	return out, err
}

// CountSideEffects is used by crash tests to prove nothing ran twice.
func (s *Store) CountSideEffects(ctx context.Context, tenant, runID string) (int, error) {
	var n int
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM side_effects WHERE run_id = $1`, runID).Scan(&n)
	})
	return n, err
}
