// Command aegis-ctl is the operator's control: the kill switch.
//
//	aegis-ctl -config c.yaml kill   [-tenant t] -reason "..."   # stop runs, refuse new ones and all model calls
//	aegis-ctl -config c.yaml unkill [-tenant t] -reason "..."
//	aegis-ctl -config c.yaml status [-tenant t]
//	aegis-ctl -config c.yaml reconcile [-tenant t]   # fix ledger status of runs whose workflow already closed
//
// Without -tenant the switch is global. It is set through the owner connection;
// the app role used by workers can only read it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"aegis/internal/app"
	"aegis/internal/config"
	"aegis/internal/ledger"
)

var terminal = []string{"APPROVED", "REJECTED", "APPROVAL_EXPIRED", "HALTED_RETRY_EXHAUSTED", "BLOCKED_SCOPE",
	"BLOCKED_SECURITY_VETO", "ESCALATED_HUMAN", "FAILED_SCHEMA", "KILLED", "BUDGET_EXCEEDED", "AGENT_REFUSED",
	"PROVIDER_ACCESS_DENIED"}

// killGrace is how long cancelled runs get to stop cleanly before they are
// terminated (a run whose queue has no worker can never process a cancel).
const killGrace = 20 * time.Second

func main() {
	path := flag.String("config", os.Getenv("AEGIS_CONFIG"), "config file")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: aegis-ctl -config c.yaml kill|unkill|status|reconcile [-tenant t] [-reason r]")
		os.Exit(2)
	}
	sub := flag.NewFlagSet(flag.Arg(0), flag.ExitOnError)
	tenant := sub.String("tenant", "", "tenant id (default: global)")
	reason := sub.String("reason", "", "why (recorded)")
	_ = sub.Parse(flag.Args()[1:])
	cfg, err := config.LoadFile(*path)
	check(err)
	adminDSN, err := cfg.DSN(cfg.Ledger.AdminEndpoint)
	check(err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	scope := *tenant
	if scope == "" {
		scope = "*"
	}
	who := "operator"
	if u, err := user.Current(); err == nil {
		who = u.Username
	}

	switch flag.Arg(0) {
	case "kill", "unkill":
		if *reason == "" {
			fmt.Fprintln(os.Stderr, "-reason is required")
			os.Exit(2)
		}
		on := flag.Arg(0) == "kill"
		check(ledger.SetKill(ctx, adminDSN, scope, on, *reason, who))
		fmt.Printf("kill switch %s for %s (%s)\n", map[bool]string{true: "ON", false: "OFF"}[on], scope, *reason)
		if !on {
			return
		}
		ids, err := activeRuns(ctx, adminDSN, *tenant)
		check(err)
		c, err := app.Temporal(cfg, app.Logger())
		check(err)
		defer c.Close()
		for _, r := range ids {
			if err := c.CancelWorkflow(ctx, r.id, ""); err != nil {
				fmt.Printf("  cancel %s: %v\n", r.id, err)
				continue
			}
			fmt.Printf("  cancelled %s\n", r.id)
		}
		fmt.Printf("%d running run(s) cancelled; new runs and model calls are refused\n", len(ids))
		// Runs still open after the grace period have no worker to process the
		// cancel: terminate them so "killed" really means stopped.
		deadline := time.Now().Add(killGrace)
		for _, r := range ids {
			for time.Now().Before(deadline) && workflowOpen(ctx, c, r.id) {
				time.Sleep(time.Second)
			}
			if workflowOpen(ctx, c, r.id) {
				if err := c.TerminateWorkflow(ctx, r.id, "", "kill switch: "+*reason); err == nil {
					fmt.Printf("  terminated %s (no worker processed the cancel)\n", r.id)
				}
			}
		}
		reconcile(ctx, cfg, c, ids)
	case "reconcile":
		ids, err := activeRuns(ctx, adminDSN, *tenant)
		check(err)
		c, err := app.Temporal(cfg, app.Logger())
		check(err)
		defer c.Close()
		reconcile(ctx, cfg, c, ids)
	case "status":
		conn, err := pgx.Connect(ctx, adminDSN)
		check(err)
		defer conn.Close(ctx)
		rows, err := conn.Query(ctx, `SELECT tenant_id, active, reason, set_by, set_at FROM kill_switch ORDER BY tenant_id`)
		check(err)
		defer rows.Close()
		n := 0
		for rows.Next() {
			var t, r, by string
			var active bool
			var at time.Time
			check(rows.Scan(&t, &active, &r, &by, &at))
			fmt.Printf("%-20s %-5v %s (by %s at %s)\n", t, active, r, by, at.Format(time.RFC3339))
			n++
		}
		if n == 0 {
			fmt.Println("no kill switch has ever been set")
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown command", flag.Arg(0))
		os.Exit(2)
	}
}

type runRef struct{ id, tenant string }

// activeRuns lists non-terminal runs (owner connection: sees every tenant).
func activeRuns(ctx context.Context, dsn, tenant string) ([]runRef, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT run_id, tenant_id FROM runs WHERE status <> ALL($1) AND ($2 = '' OR tenant_id = $2)`, terminal, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []runRef
	for rows.Next() {
		var r runRef
		if err := rows.Scan(&r.id, &r.tenant); err != nil {
			return nil, err
		}
		ids = append(ids, r)
	}
	return ids, rows.Err()
}

func workflowOpen(ctx context.Context, c client.Client, id string) bool {
	d, err := c.DescribeWorkflowExecution(ctx, id, "")
	return err == nil && d.WorkflowExecutionInfo.GetStatus() == enums.WORKFLOW_EXECUTION_STATUS_RUNNING
}

// reconcile gives every run whose workflow has closed (or no longer exists) its
// real final status in the ledger, with an audit event. Runs still running are left alone.
func reconcile(ctx context.Context, cfg *config.Config, c client.Client, runs []runRef) {
	store, err := app.Ledger(ctx, cfg)
	check(err)
	defer store.Close()
	fixed := 0
	for _, r := range runs {
		status, reason := "", ""
		d, err := c.DescribeWorkflowExecution(ctx, r.id, "")
		var nf *serviceerror.NotFound
		switch {
		case errors.As(err, &nf):
			status, reason = "KILLED", "workflow no longer exists in Temporal"
		case err != nil:
			fmt.Printf("  %s: %v\n", r.id, err)
			continue
		default:
			switch d.WorkflowExecutionInfo.GetStatus() {
			case enums.WORKFLOW_EXECUTION_STATUS_RUNNING:
				continue
			case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:
				var out struct {
					Status string `json:"status"`
					Reason string `json:"reason"`
				}
				if err := c.GetWorkflow(ctx, r.id, "").Get(ctx, &out); err == nil && out.Status != "" {
					status, reason = out.Status, "workflow completed: "+out.Reason
				} else {
					status, reason = "ESCALATED_HUMAN", "workflow completed without a readable outcome"
				}
			case enums.WORKFLOW_EXECUTION_STATUS_CANCELED, enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
				status, reason = "KILLED", "workflow "+strings.ToLower(d.WorkflowExecutionInfo.GetStatus().String())
			default:
				status, reason = "ESCALATED_HUMAN", "workflow ended: "+d.WorkflowExecutionInfo.GetStatus().String()
			}
		}
		if err := store.ReconcileRun(ctx, r.tenant, r.id, status, reason); err != nil {
			fmt.Printf("  %s: %v\n", r.id, err)
			continue
		}
		fmt.Printf("  reconciled %s -> %s (%s)\n", r.id, status, reason)
		fixed++
	}
	fmt.Printf("%d run(s) reconciled\n", fixed)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
