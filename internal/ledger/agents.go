package ledger

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"aegis/internal/llm"
)

// KillActive reports whether the global or tenant kill switch is on.
func (s *Store) KillActive(ctx context.Context, tenant string) (bool, string, error) {
	var active bool
	var reason string
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT active, reason FROM kill_switch WHERE active ORDER BY tenant_id = '*' DESC LIMIT 1`).Scan(&active, &reason)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	return active, reason, err
}

// RunSpend sums tokens and known cost for a run.
func (s *Store) RunSpend(ctx context.Context, tenant, runID string) (int64, float64, error) {
	var tokens int64
	var usd float64
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens), 0),
			COALESCE(SUM(cost_usd), 0)::float8 FROM llm_calls WHERE run_id = $1`, runID).Scan(&tokens, &usd)
	})
	return tokens, usd, err
}

// TenantSpendToday sums known cost over the last 24 hours.
func (s *Store) TenantSpendToday(ctx context.Context, tenant string) (float64, error) {
	var usd float64
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(SUM(cost_usd), 0)::float8 FROM llm_calls
			WHERE tenant_id = $1 AND created_at > now() - interval '24 hours'`, tenant).Scan(&usd)
	})
	return usd, err
}

// RecordLLMCall stores one model call (metadata only).
func (s *Store) RecordLLMCall(ctx context.Context, r llm.CallRecord) error {
	return s.tx(ctx, r.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO llm_calls (tenant_id, run_id, role, attempt, seq, provider, model, prompt_hash,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, latency_ms, cost_usd, stop_reason,
			inference_geo, redactions, error) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
			r.TenantID, r.RunID, r.Role, r.Attempt, r.Seq, r.Provider, r.Model, r.PromptHash, r.InputTokens, r.OutputTokens,
			r.CacheReadTokens, r.CacheWriteTokens, r.LatencyMS, r.CostUSD, r.StopReason, r.InferenceGeo, r.Redactions, r.Error)
		return err
	})
}

// ToolCall is one audited gateway call.
type ToolCall struct {
	TenantID, RunID, Role, Tool, ArgsHash, Reason string
	Attempt                                       int
	Allowed                                       bool
	DurationMS                                    int64
}

// RecordToolCall stores one gateway decision.
func (s *Store) RecordToolCall(ctx context.Context, c ToolCall) error {
	return s.tx(ctx, c.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO tool_calls (tenant_id, run_id, role, attempt, tool, args_hash, allowed, reason, duration_ms)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, c.TenantID, c.RunID, c.Role, c.Attempt, c.Tool, c.ArgsHash, c.Allowed, c.Reason, c.DurationMS)
		return err
	})
}

// RunUsage summarises a run's model use for reports.
type RunUsage struct {
	Calls                                int
	InputTokens, OutputTokens, CacheRead int64
	CostUSD                              *float64
	DeniedToolCalls, ToolCalls           int
	Redactions                           int
	Regions                              []string
}

func (s *Store) RunUsage(ctx context.Context, tenant, runID string) (RunUsage, error) {
	var u RunUsage
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		var cost *float64
		if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(cache_read_tokens),0), CASE WHEN bool_and(cost_usd IS NOT NULL) THEN SUM(cost_usd)::float8 END,
			COALESCE(SUM(redactions),0), COALESCE(array_agg(DISTINCT inference_geo) FILTER (WHERE inference_geo <> ''), '{}')
			FROM llm_calls WHERE run_id = $1 AND error = ''`, runID).Scan(&u.Calls, &u.InputTokens, &u.OutputTokens,
			&u.CacheRead, &cost, &u.Redactions, &u.Regions); err != nil {
			return err
		}
		u.CostUSD = cost
		return tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE NOT allowed) FROM tool_calls WHERE run_id = $1`, runID).
			Scan(&u.ToolCalls, &u.DeniedToolCalls)
	})
	return u, err
}

// SetKill turns a kill switch on or off. Owner (admin) connection only.
func SetKill(ctx context.Context, adminDSN, scope string, active bool, reason, by string) error {
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `INSERT INTO kill_switch (tenant_id, active, reason, set_by, set_at) VALUES ($1,$2,$3,$4,now())
		ON CONFLICT (tenant_id) DO UPDATE SET active = EXCLUDED.active, reason = EXCLUDED.reason, set_by = EXCLUDED.set_by, set_at = now()`,
		scope, active, reason, by)
	return err
}
