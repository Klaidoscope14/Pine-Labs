// Package llm is the only path from AegisSDLC to a model provider.
//
// Every call goes through Router.Complete, which, in order: refuses if the kill
// switch is on, refuses if the run or tenant budget is spent, applies the role's
// model/effort/max_tokens from config, refuses (fails closed) if the outgoing
// request still contains sensitive data, tries providers in fallback order with
// retries, records tokens/latency/cost/region to the ledger, and turns a refusal
// into an explicit error. No prompt or completion text is ever logged.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"gopkg.in/yaml.v3"

	"aegis/internal/config"
	"aegis/internal/dlp"
)

// Errors callers act on.
var (
	ErrKilled        = errors.New("kill switch is on")
	ErrBudget        = errors.New("budget exhausted")
	ErrRefused       = errors.New("model refused the request")
	ErrSensitiveData = errors.New("request still contains sensitive data")
	ErrUnavailable   = errors.New("all model providers unavailable")
	ErrNoCassette    = errors.New("no recorded response for this call")
)

// Call is one model request from one agent.
type Call struct {
	TenantID string
	RunID    string
	Role     string // analyst | architect | developer | release
	Attempt  int
	// Seq numbers the model calls within one agent invocation; with Cassette it
	// makes replay deterministic regardless of Temporal retries.
	Seq      int
	Cassette string
	// PromptHash identifies the prompt template version.
	PromptHash string
	// Redactions is how many values the caller redacted while building the request.
	Redactions int
	Params     anthropic.MessageNewParams
}

// Result is a successful response.
type Result struct {
	Message  *anthropic.Message
	Provider string
	Model    string
	CostUSD  *float64
}

// CallRecord is what the ledger stores for each attempt at a call.
type CallRecord struct {
	TenantID, RunID, Role, Provider, Model, StopReason, PromptHash, InferenceGeo, Error string
	Attempt, Seq, Redactions                                                            int
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens, LatencyMS             int64
	CostUSD                                                                             *float64
}

// Ledger is the subset of the ledger the router needs.
type Ledger interface {
	KillActive(ctx context.Context, tenant string) (bool, string, error)
	RunSpend(ctx context.Context, tenant, runID string) (tokens int64, usd float64, err error)
	TenantSpendToday(ctx context.Context, tenant string) (usd float64, err error)
	RecordLLMCall(ctx context.Context, rec CallRecord) error
}

// Provider sends one request. Implementations: SDK (Bedrock / first-party),
// Recorder (wraps an SDK provider), Replay.
type Provider interface {
	Name() string
	Model(role string) string
	Send(ctx context.Context, call Call, params anthropic.MessageNewParams) (*anthropic.Message, error)
}

// Price is per million tokens, in USD.
type Price struct {
	Input      float64 `yaml:"input_per_mtok"`
	Output     float64 `yaml:"output_per_mtok"`
	CacheRead  float64 `yaml:"cache_read_per_mtok"`
	CacheWrite float64 `yaml:"cache_write_per_mtok"`
}

// LoadPrices reads a price table file: {models: {<model-id>: Price}}.
// A missing file or model means cost is recorded as unknown, never guessed.
func LoadPrices(path string) (map[string]Price, error) {
	if path == "" {
		return map[string]Price{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Models map[string]Price `yaml:"models"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return doc.Models, nil
}

// Router enforces every rule above.
type Router struct {
	Providers []Provider // in fallback order
	Ledger    Ledger
	Roles     map[string]config.RoleSettings
	Budget    config.Budget
	Prices    map[string]Price
	Timeout   time.Duration
	Now       func() time.Time
}

func cost(p Price, ok bool, u anthropic.Usage) *float64 {
	if !ok {
		return nil
	}
	c := (float64(u.InputTokens)*p.Input + float64(u.OutputTokens)*p.Output +
		float64(u.CacheReadInputTokens)*p.CacheRead + float64(u.CacheCreationInputTokens)*p.CacheWrite) / 1e6
	return &c
}

// retryable reports whether a provider error should fall through to the next provider.
func retryable(err error) bool {
	var apierr *anthropic.Error
	if errors.As(err, &apierr) {
		return apierr.StatusCode == 408 || apierr.StatusCode == 409 || apierr.StatusCode == 429 || apierr.StatusCode >= 500
	}
	var herr *HTTPError
	if errors.As(err, &herr) {
		return herr.StatusCode == 408 || herr.StatusCode == 409 || herr.StatusCode == 429 || herr.StatusCode >= 500
	}
	var nerr net.Error
	return errors.As(err, &nerr) || errors.Is(err, context.DeadlineExceeded)
}

// Complete runs one call through every guard.
func (r *Router) Complete(ctx context.Context, call Call) (Result, error) {
	if killed, reason, err := r.Ledger.KillActive(ctx, call.TenantID); err != nil {
		return Result{}, fmt.Errorf("kill switch check: %w", err)
	} else if killed {
		return Result{}, fmt.Errorf("%w: %s", ErrKilled, reason)
	}
	tokens, usd, err := r.Ledger.RunSpend(ctx, call.TenantID, call.RunID)
	if err != nil {
		return Result{}, fmt.Errorf("budget check: %w", err)
	}
	if r.Budget.PerRunTokens > 0 && tokens >= int64(r.Budget.PerRunTokens) {
		return Result{}, fmt.Errorf("%w: run used %d tokens (limit %d)", ErrBudget, tokens, r.Budget.PerRunTokens)
	}
	if r.Budget.PerRunUSD > 0 && usd >= r.Budget.PerRunUSD {
		return Result{}, fmt.Errorf("%w: run spent $%.2f (limit $%.2f)", ErrBudget, usd, r.Budget.PerRunUSD)
	}
	if day, err := r.Ledger.TenantSpendToday(ctx, call.TenantID); err != nil {
		return Result{}, fmt.Errorf("budget check: %w", err)
	} else if r.Budget.PerTenantDailyUSD > 0 && day >= r.Budget.PerTenantDailyUSD {
		return Result{}, fmt.Errorf("%w: tenant spent $%.2f today (limit $%.2f)", ErrBudget, day, r.Budget.PerTenantDailyUSD)
	}

	role, ok := r.Roles[call.Role]
	if !ok {
		return Result{}, fmt.Errorf("no settings for role %q", call.Role)
	}
	params := call.Params
	params.MaxTokens = int64(role.MaxTokens)
	params.OutputConfig.Effort = anthropic.OutputConfigEffort(role.Effort)

	// Fail closed: the agent layer redacts while building the request; anything
	// still detectable here means a path skipped redaction, so nothing is sent.
	if f, err := scanRequest(params); err != nil {
		return Result{}, err
	} else if f.Total() > 0 {
		return Result{}, fmt.Errorf("%w: %v", ErrSensitiveData, f)
	}

	var lastErr error
	for _, p := range r.Providers {
		params.Model = anthropic.Model(p.Model(call.Role))
		cctx, cancel := context.WithTimeout(ctx, r.Timeout)
		start := r.now()
		msg, err := p.Send(cctx, call, params)
		cancel()
		latency := r.now().Sub(start).Milliseconds()
		rec := CallRecord{TenantID: call.TenantID, RunID: call.RunID, Role: call.Role, Provider: p.Name(),
			Model: string(params.Model), PromptHash: call.PromptHash, Attempt: call.Attempt, Seq: call.Seq,
			Redactions: call.Redactions, LatencyMS: latency}
		if err != nil {
			rec.Error = truncate(err.Error(), 300)
			_ = r.Ledger.RecordLLMCall(ctx, rec)
			if errors.Is(err, ErrNoCassette) || !retryable(err) {
				return Result{}, err
			}
			lastErr = err
			continue
		}
		price, known := r.Prices[string(params.Model)]
		rec.StopReason = string(msg.StopReason)
		rec.InputTokens, rec.OutputTokens = msg.Usage.InputTokens, msg.Usage.OutputTokens
		rec.CacheReadTokens, rec.CacheWriteTokens = msg.Usage.CacheReadInputTokens, msg.Usage.CacheCreationInputTokens
		rec.InferenceGeo = msg.Usage.InferenceGeo
		rec.CostUSD = cost(price, known, msg.Usage)
		if err := r.Ledger.RecordLLMCall(ctx, rec); err != nil {
			return Result{}, fmt.Errorf("record call: %w", err)
		}
		if msg.StopReason == anthropic.StopReasonRefusal {
			return Result{}, fmt.Errorf("%w (category %q)", ErrRefused, msg.StopDetails.Category)
		}
		return Result{Message: msg, Provider: p.Name(), Model: string(params.Model), CostUSD: rec.CostUSD}, nil
	}
	return Result{}, fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
}

func (r *Router) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// scanRequest checks every string value in the request as the model will see it.
// Scanning the JSON encoding instead would misread escapes (a newline before
// "@router.post" encodes as "\n@router.post", which looks like an email address).
func scanRequest(params anthropic.MessageNewParams) (dlp.Finding, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	total := dlp.Finding{}
	// The model's own earlier turns are not scanned: echoing what the provider
	// generated back to the same provider discloses nothing new. Everything we
	// send (system prompt, ticket, repo files, tool output) is scanned.
	if m, ok := doc.(map[string]any); ok {
		if msgs, ok := m["messages"].([]any); ok {
			kept := msgs[:0]
			for _, msg := range msgs {
				if mm, ok := msg.(map[string]any); ok && mm["role"] == "assistant" {
					continue
				}
				kept = append(kept, msg)
			}
			m["messages"] = kept
		}
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for k, n := range dlp.Scan(x) {
				total[k] += n
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(doc)
	return total, nil
}
