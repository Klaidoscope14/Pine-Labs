// Command aegis-bench turns benchmark runs into a comparison report (PLAN.md §10.7).
//
//	aegis-bench -config c.yaml -tenant t -runs ids.txt [-out report.md]
//
// ids.txt holds "label<TAB>run_id" lines (scripts/benchmark.sh writes it). For each
// label (usually a model) it reports pass rate, attempts, model calls, tokens,
// wall time, cost per run and, the number that matters, cost per successful change.
// Everything comes from the ledger, so the numbers are the audited ones.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"aegis/internal/app"
	"aegis/internal/config"
)

type agg struct {
	runs, passed, attempts, calls int
	tokens                        int64
	cost, wastedCost              float64
	costKnown                     bool
	wall                          []time.Duration
	statuses                      map[string]int
}

func main() {
	path := flag.String("config", os.Getenv("AEGIS_CONFIG"), "config file")
	tenant := flag.String("tenant", "payments-team", "tenant the runs belong to")
	runsFile := flag.String("runs", "", "file of label<TAB>run_id lines")
	out := flag.String("out", "", "write the markdown report here (default stdout)")
	flag.Parse()
	cfg, err := config.LoadFile(*path)
	check(err)
	f, err := os.Open(*runsFile)
	check(err)
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	store, err := app.Ledger(ctx, cfg)
	check(err)
	defer store.Close()

	by := map[string]*agg{}
	var order []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		label, id, ok := strings.Cut(strings.TrimSpace(sc.Text()), "\t")
		if !ok || id == "" {
			continue
		}
		a := by[label]
		if a == nil {
			a = &agg{costKnown: true, statuses: map[string]int{}}
			by[label] = a
			order = append(order, label)
		}
		meta, err := store.RunMeta(ctx, *tenant, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", id, err)
			continue
		}
		u, err := store.RunUsage(ctx, *tenant, id)
		check(err)
		a.runs++
		a.statuses[meta.Status]++
		a.attempts += meta.Attempts
		a.calls += u.Calls
		a.tokens += u.InputTokens + u.OutputTokens + u.CacheRead
		a.wall = append(a.wall, meta.UpdatedAt.Sub(meta.CreatedAt))
		ok = meta.Status == "APPROVED" || meta.Status == "AWAITING_HUMAN"
		if ok {
			a.passed++
		}
		if u.CostUSD == nil {
			a.costKnown = false
		} else {
			a.cost += *u.CostUSD
			if !ok {
				a.wastedCost += *u.CostUSD
			}
		}
	}
	check(sc.Err())

	var b strings.Builder
	fmt.Fprintf(&b, "# Model benchmark\n\nGenerated %s from the ledger (tenant %s). Same pipeline, gates and guardrails for every model.\n\n",
		time.Now().UTC().Format("2006-01-02 15:04 UTC"), *tenant)
	b.WriteString("| Model | Runs | Approved | Avg attempts | Avg calls | Avg tokens | Median wall time | Cost / run | **Cost / successful change** | Wasted spend |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, l := range order {
		a := by[l]
		if a.runs == 0 {
			continue
		}
		n := float64(a.runs)
		costRun, costOK, wasted := "unknown", "unknown", "unknown"
		if a.costKnown {
			costRun = fmt.Sprintf("$%.3f", a.cost/n)
			wasted = fmt.Sprintf("$%.3f", a.wastedCost)
			costOK = "no successes"
			if a.passed > 0 {
				costOK = fmt.Sprintf("**$%.3f**", a.cost/float64(a.passed))
			}
		}
		fmt.Fprintf(&b, "| %s | %d | %d (%.0f%%) | %.1f | %.0f | %.0fk | %s | %s | %s | %s |\n", l, a.runs, a.passed,
			100*float64(a.passed)/n, float64(a.attempts)/n, float64(a.calls)/n, float64(a.tokens)/n/1000,
			median(a.wall).Round(time.Second), costRun, costOK, wasted)
	}
	b.WriteString("\nOutcomes:\n\n")
	for _, l := range order {
		a := by[l]
		var parts []string
		for st, c := range a.statuses {
			parts = append(parts, fmt.Sprintf("%s ×%d", st, c))
		}
		sort.Strings(parts)
		fmt.Fprintf(&b, "- **%s**: %s\n", l, strings.Join(parts, ", "))
	}
	b.WriteString("\nCost per successful change = total model spend ÷ approved changes, so failed attempts count against the model. " +
		"Claude via Claude Code is billed by subscription; its cost is notional (API list price for the same tokens).\n")

	if *out == "" {
		fmt.Print(b.String())
		return
	}
	check(os.WriteFile(*out, []byte(b.String()), 0o644))
	fmt.Println("wrote", *out)
}

func median(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
