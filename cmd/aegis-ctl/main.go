// Command aegis-ctl is the operator's control: the kill switch and ledger reconcile.
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
	"flag"
	"fmt"
	"os"
	"os/user"
	"time"

	"github.com/jackc/pgx/v5"

	"aegis/internal/app"
	"aegis/internal/config"
	"aegis/internal/ops"
)

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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
		if flag.Arg(0) == "unkill" {
			check(ops.Unkill(ctx, adminDSN, scope, *reason, who))
			fmt.Printf("kill switch OFF for %s (%s)\n", scope, *reason)
			return
		}
		c, err := app.Temporal(cfg, app.Logger())
		check(err)
		defer c.Close()
		store, err := app.Ledger(ctx, cfg)
		check(err)
		defer store.Close()
		log, err := ops.Kill(ctx, adminDSN, c, store, scope, *reason, who)
		for _, l := range log {
			fmt.Println(l)
		}
		check(err)
	case "reconcile":
		ids, err := ops.ActiveRuns(ctx, adminDSN, *tenant)
		check(err)
		c, err := app.Temporal(cfg, app.Logger())
		check(err)
		defer c.Close()
		store, err := app.Ledger(ctx, cfg)
		check(err)
		defer store.Close()
		for _, l := range ops.Reconcile(ctx, c, store, ids) {
			fmt.Println(l)
		}
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

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
