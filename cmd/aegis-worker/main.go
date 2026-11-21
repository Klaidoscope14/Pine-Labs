// Command aegis-worker runs the AegisSDLC workflow and activities.
//
// In development it runs on the host because it drives Docker for sandboxes.
// Production replaces the Docker provider with a sandbox service, so the worker
// never holds Docker or cluster-admin access (PLAN.md §10.2).
package main

import (
	"context"
	"flag"
	"os"

	"go.temporal.io/sdk/worker"
	tworkflow "go.temporal.io/sdk/workflow"

	"aegis/internal/agents/stub"
	"aegis/internal/app"
	"aegis/internal/config"
	"aegis/internal/sandbox"
	"aegis/internal/workflow"
)

func main() {
	path := flag.String("config", os.Getenv("AEGIS_CONFIG"), "config file")
	flag.Parse()
	log := app.Logger()
	cfg, err := config.LoadFile(*path)
	if err != nil {
		log.Error("refusing to start: config rejected", "error", err.Error())
		os.Exit(1)
	}
	policy, err := sandbox.PolicyFromConfig(cfg.Sandbox)
	if err != nil {
		log.Error("sandbox policy", "error", err.Error())
		os.Exit(1)
	}
	ctx := context.Background()
	store, err := app.Ledger(ctx, cfg)
	if err != nil {
		log.Error("ledger", "error", err.Error())
		os.Exit(1)
	}
	defer store.Close()
	c, err := app.Temporal(cfg, log)
	if err != nil {
		log.Error("temporal", "error", err.Error())
		os.Exit(1)
	}
	defer c.Close()

	docker := sandbox.Docker{Policy: policy, MaxOutputBytes: cfg.Sandbox.MaxOutputBytes}
	if n, err := docker.Sweep(ctx); err != nil {
		log.Warn("sandbox sweep failed", "error", err.Error())
	} else if n > 0 {
		log.Info("removed orphaned sandbox containers", "count", n)
	}
	acts := &workflow.Activities{
		Cfg: cfg, Store: store, WS: app.Workspace(cfg), Image: cfg.Sandbox.Image,
		Sandbox: docker,
		Agents:  stub.Agents{Root: cfg.Path(cfg.Evals.ScenariosDir)}, // Phase 2: LLM agents
	}
	w := worker.New(c, cfg.Temporal.TaskQueue, worker.Options{
		MaxConcurrentActivityExecutionSize: cfg.Temporal.MaxConcurrentActivities,
	})
	w.RegisterWorkflowWithOptions(workflow.Run, tworkflow.RegisterOptions{Name: app.WorkflowName})
	w.RegisterActivity(acts)
	log.Info("worker started", "task_queue", cfg.Temporal.TaskQueue, "config_hash", cfg.Hash)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Error("worker stopped", "error", err.Error())
		os.Exit(1)
	}
}
