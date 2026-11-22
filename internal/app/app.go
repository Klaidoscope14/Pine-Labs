// Package app wires config into concrete dependencies for the commands.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"

	"aegis/internal/agents"
	"aegis/internal/agents/llmagents"
	"aegis/internal/agents/stub"
	"aegis/internal/config"
	"aegis/internal/ledger"
	"aegis/internal/llm"
	"aegis/internal/sandbox"
	"aegis/internal/workflow"
	"aegis/internal/workspace"
)

// Logger is the shared structured logger.
func Logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// Temporal dials the workflow endpoint from config.
func Temporal(cfg *config.Config, log *slog.Logger) (client.Client, error) {
	e, ok := cfg.Endpoint(cfg.Temporal.Endpoint)
	if !ok {
		return nil, fmt.Errorf("unknown temporal endpoint %q", cfg.Temporal.Endpoint)
	}
	host := strings.TrimPrefix(strings.TrimPrefix(e.URL, "http://"), "https://")
	return client.Dial(client.Options{HostPort: host, Namespace: cfg.Temporal.Namespace,
		Logger: tlog.NewStructuredLogger(log.With("component", "temporal"))})
}

// Ledger opens the app-role store.
func Ledger(ctx context.Context, cfg *config.Config) (*ledger.Store, error) {
	dsn, err := cfg.DSN(cfg.Ledger.AppEndpoint)
	if err != nil {
		return nil, err
	}
	return ledger.Open(ctx, dsn)
}

// Workspace builds the workspace manager.
func Workspace(cfg *config.Config) workspace.Manager {
	return workspace.Manager{MirrorsDir: cfg.Path(cfg.Workspace.MirrorsDir), RunsDir: cfg.Path(cfg.Workspace.RunsDir),
		AuthorName: cfg.Workspace.AuthorName, AuthorMail: cfg.Workspace.AuthorEmail}
}

// Settings snapshots the config values a run executes under.
func Settings(cfg *config.Config, tenant string) workflow.Settings {
	sandboxTimeout := time.Duration(cfg.Sandbox.TimeoutSeconds) * time.Second
	// A tier runs each gate on head and (uncached) base; allow for that plus queueing.
	perTier := len(cfg.Gates.Tier1) + len(cfg.Gates.Security) + len(cfg.Gates.Tier2)
	rounds := (perTier + cfg.Gates.Parallelism - 1) / cfg.Gates.Parallelism
	reviewers := 1
	for _, t := range cfg.Tenants {
		if t.ID == tenant && t.Reviewers > 0 {
			reviewers = t.Reviewers
		}
	}
	var freezes []workflow.FreezeWindow
	for _, f := range cfg.Approval.FreezeWindows {
		freezes = append(freezes, workflow.FreezeWindow{Start: f.Start, End: f.End, Reason: f.Reason})
	}
	orDefault := func(v, d int) time.Duration {
		if v <= 0 {
			v = d
		}
		return time.Duration(v) * time.Second
	}
	return workflow.Settings{
		MaxCIRounds: cfg.Workflow.MaxCIRounds,
		Tier1:       cfg.Gates.Tier1, Security: cfg.Gates.Security, Tier2: cfg.Gates.Tier2,
		ApprovalExpiry:   time.Duration(cfg.Approval.ExpirySeconds) * time.Second,
		ActivityTimeout:  sandboxTimeout * time.Duration(2*rounds+1),
		HeartbeatTimeout: time.Duration(cfg.Temporal.HeartbeatSeconds) * time.Second,

		MaxArbitrationRounds: cfg.Workflow.MaxArbitrationRounds,
		RequiredApprovers:    cfg.Review.RequiredApprovers,
		FreezeWindows:        freezes,
		LeaseTTL:             orDefault(cfg.Scope.LeaseTTLSeconds, 1800),
		LeaseWait:            orDefault(cfg.Scope.LeaseWaitSeconds, 1800),
		LeasePoll:            15 * time.Second,
		ReviewWIPCap:         cfg.Review.WIPPerReviewer * reviewers,
		ReviewPoll:           orDefault(cfg.Review.ReviewPollSeconds, 30),
		PauseRetry:           orDefault(cfg.LLM.PauseRetrySeconds, 300),
		MaxRebases:           3,
		UsePolicy:            cfg.Approval.PolicyDir != "",
		MergeQueue:           cfg.Workflow.MergeQueue,
	}
}

// IntakeKey is the idempotent intake key (PLAN.md §16.2): the same requirement
// for the same tenant and repo, however it is spaced or capitalised, maps to the
// same run id, so a duplicate submission attaches to the existing run.
func IntakeKey(tenant, repo, requirement string) string {
	norm := strings.ToLower(strings.Join(strings.Fields(requirement), " "))
	sum := sha256.Sum256([]byte(tenant + "\x00" + repo + "\x00" + norm))
	return "intake-" + hex.EncodeToString(sum[:8])
}

// TaskQueue is the queue a worker serves and runs are sent to. Real-agent
// workers get one queue per primary provider, so workers for different models
// can run side by side without picking up each other's runs.
func TaskQueue(cfg *config.Config) string {
	if cfg.Agents.Mode == "llm" && len(cfg.LLM.FallbackOrder) > 0 {
		return cfg.Temporal.TaskQueue + "-" + cfg.LLM.FallbackOrder[0]
	}
	return cfg.Temporal.TaskQueue
}

// WorkflowName is the registered name of the blueprint workflow.
const WorkflowName = "AegisRun"

// MergeQueueName is the registered name of the per-repo merge queue workflow.
const MergeQueueName = workflow.MergeQueueName

// Agents builds the configured agent implementation (stub or Claude-backed).
func Agents(ctx context.Context, cfg *config.Config, store *ledger.Store, sb sandbox.Provider) (agents.Agents, error) {
	if cfg.Agents.Mode != "llm" {
		return stub.Agents{Root: cfg.Path(cfg.Evals.ScenariosDir)}, nil
	}
	providers, err := llm.Build(ctx, cfg)
	if err != nil {
		return nil, err
	}
	prices := map[string]llm.Price{}
	for _, name := range cfg.LLM.FallbackOrder {
		p, _ := cfg.Provider(name)
		if p.PriceTableRef == "" {
			continue
		}
		table, err := llm.LoadPrices(cfg.Path(p.PriceTableRef))
		if err != nil {
			return nil, fmt.Errorf("price table for %s: %w", name, err)
		}
		for model, price := range table {
			prices[model] = price
		}
	}
	router := &llm.Router{Providers: providers, Ledger: store, Roles: cfg.LLM.Roles, Budget: cfg.Budget,
		Prices: prices, Timeout: 10 * time.Minute}
	audit := func(ctx context.Context, c ledger.ToolCall) { _ = store.RecordToolCall(ctx, c) }
	return llmagents.New(router, cfg, sb, cfg.Sandbox.Image, audit)
}
