// Package ops holds operator actions shared by aegis-ctl and the web API: the kill
// switch (stop runs, refuse new ones and all model calls) and ledger reconcile.
package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"aegis/internal/ledger"
)

// Terminal lists run statuses that end a run.
var Terminal = []string{"APPROVED", "REJECTED", "APPROVAL_EXPIRED", "HALTED_RETRY_EXHAUSTED", "BLOCKED_SCOPE",
	"BLOCKED_SECURITY_VETO", "ESCALATED_HUMAN", "FAILED_SCHEMA", "KILLED", "BUDGET_EXCEEDED", "AGENT_REFUSED",
	"PROVIDER_ACCESS_DENIED", "MERGED", "MERGE_FAILED"}

// KillGrace is how long cancelled runs get to stop cleanly before they are
// terminated (a run whose queue has no worker can never process a cancel).
const KillGrace = 20 * time.Second

// RunRef identifies a run.
type RunRef struct{ ID, Tenant string }

// ActiveRuns lists non-terminal runs (owner connection: sees every tenant).
func ActiveRuns(ctx context.Context, adminDSN, tenant string) ([]RunRef, error) {
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT run_id, tenant_id FROM runs WHERE status <> ALL($1) AND ($2 = '' OR tenant_id = $2)`, Terminal, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []RunRef
	for rows.Next() {
		var r RunRef
		if err := rows.Scan(&r.ID, &r.Tenant); err != nil {
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

// Kill turns the kill switch on for scope ("*" = global, else a tenant), cancels
// the active runs, terminates any no worker stopped within KillGrace, and
// reconciles the ledger. It returns a human-readable log.
func Kill(ctx context.Context, adminDSN string, c client.Client, store *ledger.Store, scope, reason, who string) ([]string, error) {
	if err := ledger.SetKill(ctx, adminDSN, scope, true, reason, who); err != nil {
		return nil, err
	}
	log := []string{fmt.Sprintf("kill switch ON for %s (%s)", scope, reason)}
	tenant := scope
	if tenant == "*" {
		tenant = ""
	}
	ids, err := ActiveRuns(ctx, adminDSN, tenant)
	if err != nil {
		return log, err
	}
	for _, r := range ids {
		if err := c.CancelWorkflow(ctx, r.ID, ""); err != nil {
			log = append(log, fmt.Sprintf("  cancel %s: %v", r.ID, err))
			continue
		}
		log = append(log, "  cancelled "+r.ID)
	}
	log = append(log, fmt.Sprintf("%d running run(s) cancelled; new runs and model calls are refused", len(ids)))
	deadline := time.Now().Add(KillGrace)
	for _, r := range ids {
		for time.Now().Before(deadline) && workflowOpen(ctx, c, r.ID) {
			time.Sleep(time.Second)
		}
		if workflowOpen(ctx, c, r.ID) {
			if err := c.TerminateWorkflow(ctx, r.ID, "", "kill switch: "+reason); err == nil {
				log = append(log, fmt.Sprintf("  terminated %s (no worker processed the cancel)", r.ID))
			}
		}
	}
	return append(log, Reconcile(ctx, c, store, ids)...), nil
}

// Unkill turns the kill switch off for scope.
func Unkill(ctx context.Context, adminDSN, scope, reason, who string) error {
	return ledger.SetKill(ctx, adminDSN, scope, false, reason, who)
}

// Reconcile gives every run whose workflow has closed (or no longer exists) its
// real final status in the ledger, with an audit event. Runs still running are left alone.
func Reconcile(ctx context.Context, c client.Client, store *ledger.Store, runs []RunRef) []string {
	var log []string
	fixed := 0
	for _, r := range runs {
		status, reason := "", ""
		d, err := c.DescribeWorkflowExecution(ctx, r.ID, "")
		var nf *serviceerror.NotFound
		switch {
		case errors.As(err, &nf):
			status, reason = "KILLED", "workflow no longer exists in Temporal"
		case err != nil:
			log = append(log, fmt.Sprintf("  %s: %v", r.ID, err))
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
				if err := c.GetWorkflow(ctx, r.ID, "").Get(ctx, &out); err == nil && out.Status != "" {
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
		if err := store.ReconcileRun(ctx, r.Tenant, r.ID, status, reason); err != nil {
			log = append(log, fmt.Sprintf("  %s: %v", r.ID, err))
			continue
		}
		log = append(log, fmt.Sprintf("  reconciled %s -> %s (%s)", r.ID, status, reason))
		fixed++
	}
	return append(log, fmt.Sprintf("%d run(s) reconciled", fixed))
}
