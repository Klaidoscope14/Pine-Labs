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

	"aegis/internal/app"
	"aegis/internal/config"
	approval "aegis/internal/policy"
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
	for _, ex := range cfg.ActiveExceptions() {
		log.Warn("data residency exception in force", "endpoint", ex.Endpoint, "reason", ex.Reason, "expires", ex.Expires)
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

	if err := sandbox.CheckHostFits(cfg.Sandbox); err != nil {
		log.Error("sandbox limits do not fit this host", "error", err.Error())
		os.Exit(1)
	}
	docker := sandbox.Docker{Policy: policy, MaxOutputBytes: cfg.Sandbox.MaxOutputBytes,
		Slots: sandbox.NewSlots(cfg.Sandbox.MaxConcurrent)}
	if n, err := docker.Sweep(ctx); err != nil {
		log.Warn("sandbox sweep failed", "error", err.Error())
	} else if n > 0 {
		log.Info("removed orphaned sandbox containers", "count", n)
	}
	ag, err := app.Agents(ctx, cfg, store, docker)
	if err != nil {
		log.Error("agents", "error", err.Error())
		os.Exit(1)
	}
	var pol *approval.Engine
	if cfg.Approval.PolicyDir != "" {
		if pol, err = approval.Load(ctx, cfg.Path(cfg.Approval.PolicyDir)); err != nil {
			log.Error("approval policy", "error", err.Error())
			os.Exit(1)
		}
		log.Info("approval policy loaded", "dir", cfg.Approval.PolicyDir, "hash", pol.Hash, "require_passkey", cfg.Approval.RequirePasskey)
	}
	acts := &workflow.Activities{
		Policy: pol, Temporal: c,
		Cfg: cfg, Store: store, WS: app.Workspace(cfg), Image: cfg.Sandbox.Image,
		Sandbox: docker,
		Agents:  ag,
	}
	w := worker.New(c, app.TaskQueue(cfg), worker.Options{
		MaxConcurrentActivityExecutionSize: cfg.Temporal.MaxConcurrentActivities,
	})
	w.RegisterWorkflowWithOptions(workflow.Run, tworkflow.RegisterOptions{Name: app.WorkflowName})
	w.RegisterWorkflowWithOptions(workflow.MergeQueue, tworkflow.RegisterOptions{Name: app.MergeQueueName})
	w.RegisterActivity(acts)
	log.Info("worker started", "task_queue", app.TaskQueue(cfg), "config_hash", cfg.Hash,
		"agents", cfg.Agents.Mode, "providers", cfg.LLM.FallbackOrder)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Error("worker stopped", "error", err.Error())
		os.Exit(1)
	}
}
