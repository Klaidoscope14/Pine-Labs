package llmagents

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
)

// fileStat is one file's share of the change.
type fileStat struct {
	Path  string
	Lines int
}

// diffStat measures the working tree against the base commit the same way the
// scope gate does (added + deleted lines per file, new files included).
func diffStat(ctx context.Context, worktree, base string) ([]fileStat, error) {
	// Mark new files so `git diff <base>` counts them (index only; the commit step adds everything anyway).
	if out, err := exec.CommandContext(ctx, "git", "-C", worktree, "add", "--intent-to-add", "--all").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git add -N: %v: %s", err, out)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", worktree, "diff", "--numstat", "--no-renames", base, "--").Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %v", err)
	}
	var stats []fileStat
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		added, _ := strconv.Atoi(parts[0]) // "-" for binary files counts as 0
		deleted, _ := strconv.Atoi(parts[1])
		stats = append(stats, fileStat{Path: parts[2], Lines: added + deleted})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Lines > stats[j].Lines })
	return stats, nil
}

func isTest(p string) bool {
	base := path.Base(p)
	return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") ||
		strings.HasPrefix(p, "tests/") || strings.Contains(p, "/tests/")
}

func totals(stats []fileStat) (lines, testLines int) {
	for _, s := range stats {
		lines += s.Lines
		if isTest(s.Path) {
			testLines += s.Lines
		}
	}
	return lines, testLines
}

// diffNote is the one-line size report appended to every successful edit.
func diffNote(stats []fileStat, budgetFiles, budgetLines int) string {
	lines, tests := totals(stats)
	note := fmt.Sprintf("change so far: %d lines in %d files (tests: %d lines, code: %d lines)", lines, len(stats), tests, lines-tests)
	if budgetLines > 0 {
		note += fmt.Sprintf("; diff budget %d lines, %d files", budgetLines, budgetFiles)
		if lines > budgetLines {
			note += fmt.Sprintf(" — OVER by %d lines: shrink the change before finishing", lines-budgetLines)
		} else {
			note += fmt.Sprintf(" (%d lines left)", budgetLines-lines)
		}
	}
	return note
}

// budgetProblem reports a diff over budget, naming the largest files.
func budgetProblem(stats []fileStat, budgetFiles, budgetLines int) string {
	var b strings.Builder
	lines, tests := totals(stats)
	if budgetLines > 0 && lines > budgetLines {
		fmt.Fprintf(&b, "- diff budget: %d lines changed, budget is %d (over by %d; tests are %d of those lines). Largest files:",
			lines, budgetLines, lines-budgetLines, tests)
		for i, s := range stats {
			if i == 4 {
				break
			}
			fmt.Fprintf(&b, " %s (%d)", s.Path, s.Lines)
			if i < len(stats)-1 && i < 3 {
				b.WriteString(",")
			}
		}
		b.WriteString(". Shrink the change: merge or parametrise similar tests, drop redundant cases and helpers, keep only what the plan needs. Do not add more code.\n")
	}
	if budgetFiles > 0 && len(stats) > budgetFiles {
		fmt.Fprintf(&b, "- diff budget: %d files changed, budget is %d.\n", len(stats), budgetFiles)
	}
	return b.String()
}

// exitCode reads the "exit code N" header the sandbox helpers write.
func exitCode(summary string) int {
	first, _, _ := strings.Cut(summary, "\n")
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(first, "exit code")))
	if err != nil {
		return -1
	}
	return n
}

// body drops the "exit code N" header line.
func body(summary string) string {
	_, rest, _ := strings.Cut(summary, "\n")
	return strings.TrimSpace(rest)
}

// selfCheck runs the cheap checks the gates would run and returns what fails.
func (a *Agents) selfCheck(worktree, base string, budgetFiles, budgetLines int) func(context.Context) string {
	return func(ctx context.Context) string {
		var b strings.Builder
		if stats, err := diffStat(ctx, worktree, base); err == nil {
			if len(stats) == 0 {
				b.WriteString("- the change is empty.\n")
			}
			b.WriteString(budgetProblem(stats, budgetFiles, budgetLines))
		}
		if out, err := a.runLint(worktree)(ctx, ""); err == nil && exitCode(out) != 0 {
			fmt.Fprintf(&b, "- lint (ruff) fails:\n%s\n", body(out))
		}
		if out, err := a.runTests(worktree)(ctx, ""); err == nil && exitCode(out) != 0 {
			fmt.Fprintf(&b, "- tests fail:\n%s\n", body(out))
		}
		return b.String()
	}
}

// diffNoter returns the gateway's DiffNote hook.
func diffNoter(worktree, base string, budgetFiles, budgetLines int) func(context.Context) string {
	return func(ctx context.Context) string {
		stats, err := diffStat(ctx, worktree, base)
		if err != nil {
			return ""
		}
		return diffNote(stats, budgetFiles, budgetLines)
	}
}
