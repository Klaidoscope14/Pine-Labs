// Command aegis-ledger manages and inspects the ledger.
//
//	aegis-ledger -config c.yaml migrate
//	aegis-ledger -config c.yaml verify [-tenant t]     # all configured tenants by default
//	aegis-ledger -config c.yaml show -tenant t -run r  # JSON summary
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"aegis/internal/app"
	"aegis/internal/config"
	"aegis/internal/ledger"
)

func main() {
	path := flag.String("config", os.Getenv("AEGIS_CONFIG"), "config file")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: aegis-ledger -config c.yaml migrate|verify|show [flags]")
		os.Exit(2)
	}
	cfg, err := config.LoadFile(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx := context.Background()
	sub := flag.NewFlagSet(flag.Arg(0), flag.ExitOnError)
	tenant := sub.String("tenant", "", "tenant id")
	run := sub.String("run", "", "run id")
	_ = sub.Parse(flag.Args()[1:])

	switch flag.Arg(0) {
	case "migrate":
		admin, err := cfg.DSN(cfg.Ledger.AdminEndpoint)
		check(err)
		pw, err := config.ResolveSecret(cfg.Ledger.AppPasswordRef)
		check(err)
		check(ledger.Migrate(ctx, admin, cfg.Ledger.AppRole, pw))
		fmt.Println("ledger migrated")
	case "verify":
		store, err := app.Ledger(ctx, cfg)
		check(err)
		defer store.Close()
		tenants := []string{*tenant}
		if *tenant == "" {
			tenants = nil
			for _, t := range cfg.Tenants {
				tenants = append(tenants, t.ID)
			}
		}
		bad := false
		for _, t := range tenants {
			n, err := store.Verify(ctx, t)
			if err != nil {
				fmt.Printf("%s: BROKEN: %v\n", t, err)
				bad = true
				continue
			}
			fmt.Printf("%s: verified (%d events)\n", t, n)
		}
		if bad {
			os.Exit(1)
		}
	case "show":
		store, err := app.Ledger(ctx, cfg)
		check(err)
		defer store.Close()
		sum, err := store.Summary(ctx, *tenant, *run)
		check(err)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		check(enc.Encode(sum))
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
