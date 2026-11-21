// Package app wires config into concrete dependencies for the commands.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"

	"aegis/internal/config"
	"aegis/internal/ledger"
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
func Settings(cfg *config.Config) workflow.Settings {
	sandboxTimeout := time.Duration(cfg.Sandbox.TimeoutSeconds) * time.Second
	// A tier runs each gate on head and (uncached) base; allow for that plus queueing.
	perTier := len(cfg.Gates.Tier1) + len(cfg.Gates.Security) + len(cfg.Gates.Tier2)
	rounds := (perTier + cfg.Gates.Parallelism - 1) / cfg.Gates.Parallelism
	return workflow.Settings{
		MaxCIRounds: cfg.Workflow.MaxCIRounds,
		Tier1:       cfg.Gates.Tier1, Security: cfg.Gates.Security, Tier2: cfg.Gates.Tier2,
		ApprovalExpiry:   time.Duration(cfg.Approval.ExpirySeconds) * time.Second,
		ActivityTimeout:  sandboxTimeout * time.Duration(2*rounds+1),
		HeartbeatTimeout: time.Duration(cfg.Temporal.HeartbeatSeconds) * time.Second,
	}
}

// WorkflowName is the registered name of the blueprint workflow.
const WorkflowName = "AegisRun"
