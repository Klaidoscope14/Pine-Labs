// Command aegis-ctl is the operator's control: the kill switch.
//
//	aegis-ctl -config c.yaml kill   [-tenant t] -reason "..."   # stop runs, refuse new ones and all model calls
//	aegis-ctl -config c.yaml unkill [-tenant t] -reason "..."
//	aegis-ctl -config c.yaml status [-tenant t]
//
// Without -tenant the switch is global. It is set through the owner connection;
// the app role used by workers can only read it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/user"
	"time"

	"github.com/jackc/pgx/v5"

	"aegis/internal/app"
	"aegis/internal/config"
	"aegis/internal/ledger"
)

var terminal = []string{"APPROVED", "REJECTED", "APPROVAL_EXPIRED", "HALTED_RETRY_EXHAUSTED", "BLOCKED_SCOPE",
	"BLOCKED_SECURITY_VETO", "ESCALATED_HUMAN", "FAILED_SCHEMA", "KILLED", "BUDGET_EXCEEDED", "AGENT_REFUSED",
	"PROVIDER_ACCESS_DENIED"}

func main() {
	path := flag.String("config", os.Getenv("AEGIS_CONFIG"), "config file")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: aegis-ctl -config c.yaml kill|unkill|status [-tenant t] [-reason r]")
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
		for _, id := range ids {
			if err := c.CancelWorkflow(ctx, id, ""); err != nil {
				fmt.Printf("  cancel %s: %v\n", id, err)
				continue
			}
			fmt.Printf("  cancelled %s\n", id)
		}
		fmt.Printf("%d running run(s) cancelled; new runs and model calls are refused\n", len(ids))
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

// activeRuns lists non-terminal runs (owner connection: sees every tenant).
func activeRuns(ctx context.Context, dsn, tenant string) ([]string, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT run_id FROM runs WHERE status <> ALL($1) AND ($2 = '' OR tenant_id = $2)`, terminal, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
