// Command aegis-run starts scenario runs and checks they end as expected.
//
//	aegis-run -config c.yaml -scenario happy        # one scenario, wait, verify
//	aegis-run -config c.yaml -scenario all          # every scenario concurrently
//	aegis-run -config c.yaml -scenario happy -detach  # print the run id and exit
//	aegis-run -config c.yaml -follow <run-id> -scenario happy  # resume following a run
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"go.temporal.io/sdk/client"

	"aegis/internal/app"
	"aegis/internal/config"
	"aegis/internal/ledger"
	"aegis/internal/scenario"
	"aegis/internal/workflow"
)

type result struct {
	scenario string
	runID    string
	tenant   string
	out      workflow.Outcome
	problems []string
}

func main() {
	path := flag.String("config", os.Getenv("AEGIS_CONFIG"), "config file")
	name := flag.String("scenario", "", "scenario name, or 'all'")
	detach := flag.Bool("detach", false, "start the run, print its id, and exit")
	follow := flag.String("follow", "", "follow an existing run id instead of starting one")
	timeout := flag.Duration("timeout", 30*time.Minute, "overall timeout")
	flag.Parse()
	log := app.Logger()
	cfg, err := config.LoadFile(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root := cfg.Path(cfg.Evals.ScenariosDir)
	names := []string{*name}
	if *name == "all" {
		all, err := scenario.All(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// "all" means every scenario written for the configured agent mode.
		names = nil
		for _, n := range all {
			if s, err := scenario.Load(root, n); err == nil && s.Agents == cfg.Agents.Mode {
				names = append(names, n)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	c, err := app.Temporal(cfg, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer c.Close()
	store, err := app.Ledger(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer store.Close()

	reviewer := "cli"
	if u, err := user.Current(); err == nil {
		reviewer = "cli:" + u.Username
	}
	var mu sync.Mutex
	var results []result
	var wg sync.WaitGroup
	for _, n := range names {
		s, err := scenario.Load(root, n)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if s.Agents != cfg.Agents.Mode {
			fmt.Fprintf(os.Stderr, "scenario %s is for %s agents, but this config runs %s agents\n", n, s.Agents, cfg.Agents.Mode)
			os.Exit(1)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := runOne(ctx, cfg, c, store, s, *follow, *detach, reviewer)
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if *detach {
		return
	}
	sort.Slice(results, func(i, j int) bool { return results[i].scenario < results[j].scenario })
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SCENARIO\tRUN\tSTATUS\tATTEMPTS\tMODEL CALLS\tTOKENS (in/out/cached)\tCOST\tDENIED TOOL CALLS\tRESULT")
	failed := false
	for _, r := range results {
		verdict := "ok"
		if len(r.problems) > 0 {
			verdict = "FAIL: " + strings.Join(r.problems, "; ")
			failed = true
		}
		u, _ := store.RunUsage(ctx, r.tenant, r.runID)
		cost := "n/a"
		if u.CostUSD != nil {
			cost = fmt.Sprintf("$%.4f", *u.CostUSD)
		} else if u.Calls > 0 {
			cost = "unknown (no price table)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d/%d/%d\t%s\t%d\t%s\n", r.scenario, r.runID, r.out.Status, r.out.Attempts,
			u.Calls, u.InputTokens, u.OutputTokens, u.CacheRead, cost, u.DeniedToolCalls, verdict)
	}
	tw.Flush()
	tenants := map[string]bool{}
	for _, r := range results {
		tenants[r.tenant] = true
	}
	for t := range tenants {
		n, err := store.Verify(ctx, t)
		if err != nil {
			fmt.Printf("audit chain %s: BROKEN: %v\n", t, err)
			failed = true
		} else {
			fmt.Printf("audit chain %s: verified (%d events)\n", t, n)
		}
	}
	if failed {
		os.Exit(1)
	}
}

func newRunID(s string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s", s, hex.EncodeToString(b))
}

func runOne(ctx context.Context, cfg *config.Config, c client.Client, store *ledger.Store, s scenario.Scenario,
	follow string, detach bool, reviewer string) result {
	res := result{scenario: s.Name, tenant: s.Tenant}
	fail := func(format string, args ...any) result {
		res.problems = append(res.problems, fmt.Sprintf(format, args...))
		return res
	}
	repo := cfg.Repos[0].Name
	if follow == "" {
		requirement, err := s.RequirementText(cfg.Path(cfg.Evals.ScenariosDir))
		if err != nil {
			return fail("read requirement: %v", err)
		}
		res.runID = newRunID(s.Name)
		in := workflow.Input{RunID: res.runID, TenantID: s.Tenant, Repo: repo,
			Requirement: requirement, Scenario: s.Name,
			ConfigHash: cfg.Hash, Settings: app.Settings(cfg)}
		if _, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: res.runID, TaskQueue: app.TaskQueue(cfg)},
			app.WorkflowName, in); err != nil {
			return fail("start: %v", err)
		}
		if detach {
			fmt.Println(res.runID)
			return res
		}
	} else {
		res.runID = follow
	}

	// Act as the human reviewer when the scenario says to approve.
	signaled := false
	for {
		resp, err := c.QueryWorkflow(ctx, res.runID, "", workflow.QueryState)
		var st workflow.State
		if err == nil && resp.Get(&st) == nil {
			if st.Status == workflow.AwaitingHuman && !signaled {
				note := "scenario auto-approval"
				if !s.Approve {
					note = "scenario auto-rejection"
				}
				if err := c.SignalWorkflow(ctx, res.runID, "", workflow.SignalDecision,
					workflow.Decision{Approve: s.Approve, Reviewer: reviewer, Note: note}); err != nil {
					return fail("signal: %v", err)
				}
				signaled = true
			}
		}
		desc, err := c.DescribeWorkflowExecution(ctx, res.runID, "")
		if err == nil && desc.WorkflowExecutionInfo.GetCloseTime() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return fail("timed out waiting for the run")
		case <-time.After(time.Second):
		}
	}
	if err := c.GetWorkflow(ctx, res.runID, "").Get(ctx, &res.out); err != nil {
		return fail("run failed: %v", err)
	}
	if s.Expect.Status != "" && string(res.out.Status) != s.Expect.Status {
		fail("status %s, expected %s (%s)", res.out.Status, s.Expect.Status, res.out.Reason)
	}
	if len(s.Expect.StatusIn) > 0 && !slices.Contains(s.Expect.StatusIn, string(res.out.Status)) {
		fail("status %s, expected one of %v (%s)", res.out.Status, s.Expect.StatusIn, res.out.Reason)
	}
	if s.Expect.Attempts > 0 && res.out.Attempts != s.Expect.Attempts {
		fail("attempts %d, expected %d", res.out.Attempts, s.Expect.Attempts)
	}
	sum, err := store.Summary(ctx, s.Tenant, res.runID)
	if err != nil {
		return fail("ledger: %v", err)
	}
	if sum.Run.Status != string(res.out.Status) {
		fail("ledger status %s disagrees with workflow %s", sum.Run.Status, res.out.Status)
	}
	if s.Expect.ResolvedRule != "" && !resolved(sum, s.Expect.ResolvedRule) {
		fail("finding %s was not marked RESOLVED", s.Expect.ResolvedRule)
	}
	if len(s.Expect.Untouched) > 0 && sum.Run.HeadSHA != "" {
		r, _ := cfg.Repo(repo)
		remote, _ := cfg.RepoRemote(r)
		mirror := app.Workspace(cfg).MirrorPath(remote)
		out, err := exec.Command("git", "-C", mirror, "diff", "--name-only", sum.Run.BaseSHA, sum.Run.HeadSHA, "--").Output()
		if err != nil {
			fail("diff for untouched check: %v", err)
		}
		for _, f := range strings.Fields(string(out)) {
			if slices.Contains(s.Expect.Untouched, f) {
				fail("%s was modified", f)
			}
		}
	}
	return res
}

func resolved(sum ledger.Summary, rule string) bool {
	for _, f := range sum.Findings {
		if strings.HasPrefix(f["rule_id"].(string), rule) && f["status"] == "RESOLVED" {
			return true
		}
	}
	return false
}
