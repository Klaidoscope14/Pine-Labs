// Package llmagents implements the Analyst, Architect, Developer and Release
// roles with Claude, behind the same agents.Agents interface as the stubs.
//
// Every model call goes through llm.Router (kill switch, budgets, data filter,
// fallback, ledger). Everything sent is redacted first. Every output is checked
// against its full JSON contract, with bounded repair turns. Repository text is
// passed as data, never as instructions, and the Developer can only act through
// the tool gateway.
package llmagents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/bmatcuk/doublestar/v4"

	"aegis/internal/agents"
	"aegis/internal/config"
	"aegis/internal/contracts"
	"aegis/internal/dlp"
	"aegis/internal/gateway"
	"aegis/internal/ledger"
	"aegis/internal/llm"
	"aegis/internal/sandbox"
)

// ErrContract wraps output that still fails its contract after the repair turns.
var ErrContract = errors.New("agent output failed its contract")

type prompt struct {
	text string
	hash string
}

// Agents is the Claude-backed implementation.
type Agents struct {
	Router  *llm.Router
	Cfg     *config.Config
	Sandbox sandbox.Provider
	Image   string
	// Audit records each gateway decision in the ledger.
	Audit   func(ctx context.Context, c ledger.ToolCall)
	prompts map[string]prompt
}

var _ agents.Agents = (*Agents)(nil)
var _ agents.Summarizer = (*Agents)(nil)

// New loads the versioned prompt files.
func New(router *llm.Router, cfg *config.Config, sb sandbox.Provider, image string, audit func(context.Context, ledger.ToolCall)) (*Agents, error) {
	a := &Agents{Router: router, Cfg: cfg, Sandbox: sb, Image: image, Audit: audit, prompts: map[string]prompt{}}
	for _, role := range []string{"analyst", "architect", "developer", "release"} {
		raw, err := os.ReadFile(filepath.Join(cfg.Path(cfg.Agents.PromptsDir), role+".md"))
		if err != nil {
			return nil, fmt.Errorf("prompt %s: %w", role, err)
		}
		sum := sha256.Sum256(raw)
		a.prompts[role] = prompt{text: string(raw), hash: hex.EncodeToString(sum[:8])}
	}
	return a, nil
}

// session numbers one role's calls and accumulates redaction counts.
type session struct {
	a          *Agents
	run        agents.Run
	role       string
	seq        int
	redactions int
}

func (a *Agents) session(run agents.Run, role string) *session {
	return &session{a: a, run: run, role: role}
}

// clean redacts text before it can be sent anywhere.
func (s *session) clean(text string) string {
	out, f := dlp.Redact(text)
	s.redactions += f.Total()
	return out
}

func (s *session) system() []anthropic.TextBlockParam {
	return []anthropic.TextBlockParam{{Text: s.a.prompts[s.role].text, CacheControl: anthropic.NewCacheControlEphemeralParam()}}
}

func (s *session) call(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	return s.callEffort(ctx, "", params)
}

// callEffort makes a call with an optional effort override ("" = the role's effort).
func (s *session) callEffort(ctx context.Context, effort string, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	res, err := s.a.Router.Complete(ctx, llm.Call{TenantID: s.run.TenantID, RunID: s.run.RunID, Role: s.role,
		Attempt: s.run.Attempt, Seq: s.seq, Cassette: s.run.Scenario, PromptHash: s.a.prompts[s.role].hash,
		Redactions: s.redactions, Effort: effort, Params: params})
	s.seq++
	if err != nil {
		return nil, err
	}
	return res.Message, nil
}

func text(m *anthropic.Message) string {
	var b strings.Builder
	for _, block := range m.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

// structured asks for JSON matching schema, validates it, and repairs up to the
// configured number of times by quoting the exact validation error back.
func (s *session) structured(ctx context.Context, user string, schema map[string]any, validate func([]byte) error) (json.RawMessage, error) {
	msgs := []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))}
	var lastErr error
	for i := 0; i <= s.a.Cfg.Workflow.MaxSchemaRepairs; i++ {
		resp, err := s.call(ctx, anthropic.MessageNewParams{System: s.system(), Messages: msgs,
			OutputConfig: anthropic.OutputConfigParam{Format: anthropic.JSONOutputFormatParam{Schema: schema}}})
		if err != nil {
			return nil, err
		}
		out := []byte(text(resp))
		if resp.StopReason == anthropic.StopReasonMaxTokens {
			lastErr = errors.New("output was cut off at max_tokens")
		} else if lastErr = validate(out); lastErr == nil {
			return out, nil
		}
		msgs = append(msgs, resp.ToParam(), anthropic.NewUserMessage(anthropic.NewTextBlock(
			"That output did not satisfy the required contract: "+truncate(lastErr.Error(), 1500)+
				"\nReturn the corrected JSON only.")))
	}
	return nil, fmt.Errorf("%w (%s): %v", ErrContract, s.role, lastErr)
}

// ---- repository context (always redacted, always presented as data) ----

func skipDir(name string) bool {
	return name == ".git" || name == "__pycache__" || name == ".venv" || name == ".pytest_cache"
}

func tree(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Type()&fs.ModeSymlink == 0 {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files
}

func readSmall(root, rel string, max int) string {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > max {
		data = data[:max]
	}
	return string(data)
}

func fence(title, body string) string {
	return fmt.Sprintf("<%s>\n%s\n</%s>\n", title, strings.TrimRight(body, "\n"), title)
}

// sourcePack includes source and test files, smallest first, up to the byte budget.
func sourcePack(root string, files []string, budget int) string {
	var b strings.Builder
	used := 0
	for _, f := range files {
		if !strings.HasSuffix(f, ".py") {
			continue
		}
		body := readSmall(root, f, budget)
		if used+len(body) > budget {
			b.WriteString(fmt.Sprintf("<file path=%q omitted=\"context budget reached\"/>\n", f))
			continue
		}
		used += len(body)
		b.WriteString(fmt.Sprintf("<file path=%q>\n%s\n</file>\n", f, body))
	}
	return b.String()
}

// plannedContext returns the planned files plus the tests that mention them, and
// the files whose complete content fit in the budget.
func plannedContext(root string, planned, protected []string, budget int) (string, []string) {
	all := tree(root)
	var code, tests []string
	stems := map[string]bool{}
	for _, f := range all {
		if !strings.HasSuffix(f, ".py") || matchAny(protected, f) || !matchAny(planned, f) {
			continue
		}
		if isTest(f) {
			tests = append(tests, f)
			continue
		}
		code = append(code, f)
		stems[strings.TrimSuffix(path.Base(f), ".py")] = true
	}
	for _, f := range all {
		if !isTest(f) || !strings.HasSuffix(f, ".py") || slices.Contains(tests, f) || matchAny(protected, f) {
			continue
		}
		body := readSmall(root, f, 1<<20)
		for stem := range stems {
			if stem != "__init__" && strings.Contains(body, stem) {
				tests = append(tests, f)
				break
			}
		}
	}
	var b strings.Builder
	var full []string
	used := 0
	for _, f := range append(code, tests...) {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if used+int(info.Size()) > budget {
			fmt.Fprintf(&b, "<file path=%q omitted=\"context budget reached; use read_file\"/>\n", f)
			continue
		}
		body := readSmall(root, f, int(info.Size()))
		used += len(body)
		fmt.Fprintf(&b, "<file path=%q>\n%s\n</file>\n", f, body)
		full = append(full, f)
	}
	return b.String(), full
}

func matchAny(globs []string, p string) bool {
	for _, g := range globs {
		if ok, err := doublestar.Match(g, p); err == nil && ok {
			return true
		}
	}
	return false
}

// importGraph returns the repository's import graph, cached per tree hash
// (PLAN.md §10.5, L5): every run on the same base reuses it.
func (a *Agents) importGraph(ctx context.Context, worktree, repoName string) string {
	var cache string
	if out, err := exec.CommandContext(ctx, "git", "-C", worktree, "rev-parse", "HEAD^{tree}").Output(); err == nil {
		tree := strings.TrimSpace(string(out))
		cache = filepath.Join(a.Cfg.Path(a.Cfg.Workspace.ArtifactsDir), "repo-index", repoName, tree+".importgraph.json")
		if data, err := os.ReadFile(cache); err == nil && json.Valid(data) {
			return string(data)
		}
	}
	g := a.computeImportGraph(ctx, worktree, repoName)
	if cache != "" && json.Valid([]byte(g)) && os.MkdirAll(filepath.Dir(cache), 0o750) == nil {
		tmp := fmt.Sprintf("%s.tmp-%d", cache, time.Now().UnixNano())
		if os.WriteFile(tmp, []byte(g), 0o640) == nil {
			_ = os.Rename(tmp, cache)
		}
	}
	return g
}

func (a *Agents) computeImportGraph(ctx context.Context, worktree, repoName string) string {
	repo, _ := a.Cfg.Repo(repoName)
	script := a.Cfg.Path(a.Cfg.Agents.ImportGraphScript)
	cmd := append([]string{"python", "/tools/" + filepath.Base(script), "/work"}, repo.SourceDirs...)
	res, err := a.Sandbox.Run(ctx, sandbox.Spec{Image: a.Image, Cmd: cmd, Workdir: "/work",
		Timeout: time.Duration(a.Cfg.Sandbox.TimeoutSeconds) * time.Second,
		Mounts: []sandbox.Mount{{HostPath: worktree, ContainerPath: "/work", ReadOnly: true},
			{HostPath: filepath.Dir(script), ContainerPath: "/tools", ReadOnly: true}}})
	if err != nil || res.ExitCode != 0 || !json.Valid(res.Stdout) {
		return "(import graph unavailable)"
	}
	return string(res.Stdout)
}

// ---- Analyst ----

func (a *Agents) Analyze(ctx context.Context, in agents.AnalyzeInput) (json.RawMessage, error) {
	s := a.session(in.Run, "analyst")
	files := tree(in.Worktree)
	user := s.clean(fence("requirement", in.Requirement) +
		fence("repository_rules", readSmall(in.Worktree, ".agent-rules.md", 8000)) +
		fence("readme", readSmall(in.Worktree, "README.md", 8000)) +
		fence("files", strings.Join(files, "\n")) +
		"Write the specification for this requirement.")
	schema, err := contracts.ModelSchema("requirement_spec")
	if err != nil {
		return nil, err
	}
	return s.structured(ctx, user, schema, func(b []byte) error { return contracts.Validate("requirement_spec", b) })
}

// ---- Architect ----

type plan struct {
	Scope struct {
		Planned          []string `json:"planned"`
		Adjacent         []string `json:"adjacent"`
		ProtectedTouched []string `json:"protected_touched"`
	} `json:"scope"`
}

func (a *Agents) Architect(ctx context.Context, in agents.ArchitectInput) (json.RawMessage, error) {
	s := a.session(in.Run, "architect")
	files := tree(in.Worktree)
	user := s.clean(fence("specification", string(in.RequirementSpec)) +
		fence("protected_paths", strings.Join(a.Cfg.Scope.ProtectedPaths, "\n")) +
		fence("files", strings.Join(files, "\n")) +
		fence("import_graph", a.importGraph(ctx, in.Worktree, in.Repo)) +
		fence("source", sourcePack(in.Worktree, files, a.Cfg.Agents.MaxContextBytes)) +
		"Design the change.")
	schema, err := contracts.ModelSchema("architecture_plan")
	if err != nil {
		return nil, err
	}
	out, err := s.structured(ctx, user, schema, func(b []byte) error {
		if err := contracts.Validate("architecture_plan", b); err != nil {
			return err
		}
		var p plan
		_ = json.Unmarshal(b, &p)
		for _, f := range p.Scope.Planned {
			if strings.ContainsAny(f, "*?[") {
				return fmt.Errorf("planned path %q must be an exact file, not a glob", f)
			}
			if _, err := os.Stat(filepath.Join(in.Worktree, filepath.FromSlash(f))); err != nil {
				if _, err := os.Stat(filepath.Dir(filepath.Join(in.Worktree, filepath.FromSlash(f)))); err != nil {
					return fmt.Errorf("planned path %q does not exist and has no existing directory", f)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// A plan that needs a protected file is a design decision for a human.
	var p plan
	_ = json.Unmarshal(out, &p)
	if len(p.Scope.ProtectedTouched) > 0 {
		return out, fmt.Errorf("%w: plan needs protected paths %v", agents.ErrNeedsHuman, p.Scope.ProtectedTouched)
	}
	for _, f := range append(append([]string{}, p.Scope.Planned...), p.Scope.Adjacent...) {
		for _, prot := range a.Cfg.Scope.ProtectedPaths {
			if ok, _ := doublestar.Match(f, prot); ok || f == prot {
				return out, fmt.Errorf("%w: plan scope includes protected path %s", agents.ErrNeedsHuman, prot)
			}
		}
	}
	return out, nil
}

// ---- Developer ----

func (a *Agents) runTests(worktree string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, selector string) (string, error) {
		cmd := []string{"python", "-m", "pytest", "-q", "--tb=short", "-p", "no:cacheprovider"}
		if selector != "" {
			cmd = append(cmd, selector)
		}
		res, err := a.Sandbox.Run(ctx, sandbox.Spec{Image: a.Image, Cmd: cmd, Workdir: "/work",
			Timeout: time.Duration(a.Cfg.Sandbox.TimeoutSeconds) * time.Second,
			Mounts:  []sandbox.Mount{{HostPath: worktree, ContainerPath: "/work", ReadOnly: true}}})
		if err != nil {
			return "", err
		}
		out := string(res.Stdout) + string(res.Stderr)
		if len(out) > 3000 {
			out = "…" + out[len(out)-3000:]
		}
		return fmt.Sprintf("exit code %d\n%s", res.ExitCode, out), nil
	}
}

func (a *Agents) runLint(worktree string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, path string) (string, error) {
		target := "."
		if path != "" {
			target = path
		}
		res, err := a.Sandbox.Run(ctx, sandbox.Spec{Image: a.Image, Workdir: "/work",
			Cmd:     []string{"ruff", "check", "--no-cache", "--output-format", "concise", target},
			Timeout: time.Duration(a.Cfg.Sandbox.TimeoutSeconds) * time.Second,
			Mounts:  []sandbox.Mount{{HostPath: worktree, ContainerPath: "/work", ReadOnly: true}}})
		if err != nil {
			return "", err
		}
		out := strings.TrimSpace(string(res.Stdout) + string(res.Stderr))
		if len(out) > 3000 {
			out = out[:3000] + "\n…"
		}
		return fmt.Sprintf("exit code %d\n%s", res.ExitCode, out), nil
	}
}

func (a *Agents) Develop(ctx context.Context, in agents.DevelopInput) (agents.DevelopOutput, error) {
	s := a.session(in.Run, "developer")
	var p plan
	if err := json.Unmarshal(in.Plan, &p); err != nil {
		return agents.DevelopOutput{}, err
	}
	gw := &gateway.Gateway{Root: in.Worktree, Protected: a.Cfg.Scope.ProtectedPaths,
		Planned: p.Scope.Planned, Adjacent: p.Scope.Adjacent, Writable: true,
		MaxRead: a.Cfg.Agents.MaxReadBytes, MaxCalls: a.Cfg.Agents.MaxToolCalls, MaxRunTests: a.Cfg.Agents.MaxRunTests,
		RunTests:          a.runTests(in.Worktree),
		RunLint:           a.runLint(in.Worktree),
		MaxFinishRefusals: a.Cfg.Agents.MaxFinishRefusals,
		Audit: func(ctx context.Context, c gateway.Call) {
			if a.Audit != nil {
				a.Audit(ctx, ledger.ToolCall{TenantID: in.TenantID, RunID: in.RunID, Role: "developer", Attempt: in.Attempt,
					Tool: c.Tool, ArgsHash: c.ArgsHash, Allowed: c.Allowed, Reason: c.Reason, DurationMS: c.DurationMS})
			}
		}}
	if in.BaseSHA != "" {
		gw.DiffNote = diffNoter(in.Worktree, in.BaseSHA, in.BudgetFiles, in.BudgetLines)
		check := a.selfCheck(in.Worktree, in.BaseSHA, in.BudgetFiles, in.BudgetLines)
		if in.SelfCheck != nil {
			// The real gates (scope, lint, tests), plus the size breakdown that tells
			// the model which files to shrink.
			check = func(ctx context.Context) string {
				var b strings.Builder
				if stats, err := diffStat(ctx, in.Worktree, in.BaseSHA); err == nil {
					b.WriteString(budgetProblem(stats, in.BudgetFiles, in.BudgetLines))
				}
				if d := in.SelfCheck(ctx); d != "" {
					b.WriteString(d + "\n")
				}
				return b.String()
			}
		}
		gw.Check = check
		if a.Cfg.Agents.MaxFinishRefusals > 0 {
			gw.PreFinish = check
		}
	}
	brief := fence("specification", string(in.RequirementSpec)) + fence("plan", string(in.Plan))
	// PLAN.md §10.5 L6: the planned files and the tests that cover them, up front,
	// so the loop starts editing instead of spending turns reading.
	if pack, full := plannedContext(in.Worktree, p.Scope.Planned, a.Cfg.Scope.ProtectedPaths, a.Cfg.Agents.MaxContextBytes/4); pack != "" {
		brief += fence("planned_files", pack) +
			"These are the current, complete contents of the planned files and their tests; you may edit them directly without reading them again.\n"
		gw.MarkRead(full)
	}
	if in.Diagnostics != "" {
		brief += fence("previous_attempt_failures", in.Diagnostics) +
			"Your previous attempt is already in the worktree. Fix exactly these failures.\n"
	}
	if in.BudgetLines > 0 {
		brief += fmt.Sprintf("Diff budget: at most %d changed lines (added + deleted, tests included) in at most %d files, measured against the base.\n",
			in.BudgetLines, in.BudgetFiles)
		if in.Diagnostics != "" {
			if stats, err := diffStat(ctx, in.Worktree, in.BaseSHA); err == nil {
				brief += diffNote(stats, in.BudgetFiles, in.BudgetLines) + ".\n"
			}
		}
	}
	brief += "Implement the plan. Start by reading the planned files."
	msgs := []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(s.clean(brief)))}
	tools := gw.Tools()
	nudged := false
	// Adaptive effort (PLAN.md §10.5, L10): think hard when planning or recovering
	// from a failure; routine reads and edits use the cheaper routine effort.
	routine := a.Cfg.LLM.Roles["developer"].RoutineEffort
	struggling := true // the first turn plans the change
	for turn := 0; turn < a.Cfg.Agents.MaxToolCalls+4; turn++ {
		effort := ""
		if !struggling {
			effort = routine
		}
		resp, err := s.callEffort(ctx, effort, anthropic.MessageNewParams{System: s.system(), Messages: msgs, Tools: tools,
			CacheControl: anthropic.NewCacheControlEphemeralParam()})
		if err != nil {
			return agents.DevelopOutput{}, err
		}
		msgs = append(msgs, resp.ToParam())
		if resp.StopReason != anthropic.StopReasonToolUse {
			if nudged {
				break
			}
			nudged = true
			struggling = true
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(
				"Continue working with the tools. When the change is complete and tested, call finish.")))
			continue
		}
		var results []anthropic.ContentBlockParamUnion
		var done *gateway.Outcome
		struggling = false
		for _, block := range resp.Content {
			tu, ok := block.AsAny().(anthropic.ToolUseBlock)
			if !ok {
				continue
			}
			out := gw.Execute(ctx, tu.Name, json.RawMessage(tu.JSON.Input.Raw()))
			if out.IsError || strings.HasPrefix(out.Content, "checks fail") || (tu.Name == "run_tests" || tu.Name == "run_lint") && exitCode(out.Content) > 0 {
				struggling = true
			}
			results = append(results, anthropic.NewToolResultBlock(tu.ID, out.Content, out.IsError))
			if (out.Done || out.Abort) && done == nil {
				o := out
				done = &o
			}
		}
		s.redactions += gw.Redactions
		gw.Redactions = 0
		msgs = append(msgs, anthropic.NewUserMessage(results...))
		if done == nil {
			continue
		}
		if done.Conflict != nil {
			conflict, _ := json.Marshal(map[string]any{"status": "DESIGN_CONFLICT", "modified_files": []string{},
				"decision_log": []map[string]string{{"decision": "Stopped instead of working around the plan", "reason": done.Conflict.Reason}},
				"conflict":     done.Conflict})
			return agents.DevelopOutput{Conflict: conflict}, nil
		}
		notes := []agents.Decision{}
		for _, d := range done.Decisions {
			notes = append(notes, agents.Decision{Decision: d.Decision, Reason: d.Reason})
		}
		if done.Abort {
			notes = append(notes, agents.Decision{Decision: "Attempt stopped by the tool gateway", Reason: firstLine(done.Content)})
		}
		return agents.DevelopOutput{EditedInPlace: true, Notes: notes}, nil
	}
	return agents.DevelopOutput{EditedInPlace: true, Notes: []agents.Decision{{Decision: "Stopped without calling finish",
		Reason: "turn limit reached; the gates will judge the current state"}}}, nil
}

// ---- Release ----

var summarySchema = map[string]any{
	"type": "object", "additionalProperties": false, "required": []any{"risks", "summary"},
	"properties": map[string]any{
		"summary": map[string]any{"type": "string"},
		"risks":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
}

func (a *Agents) Summarize(ctx context.Context, in agents.SummaryInput) (string, []string, error) {
	s := a.session(in.Run, "release")
	user := s.clean(fence("specification", string(in.RequirementSpec)) + fence("plan", string(in.Plan)) +
		fence("gate_results", strings.Join(in.GateSummaries, "\n")) + fence("diff_stat", in.DiffStat) +
		"Write the reviewer summary and risks.")
	out, err := s.structured(ctx, user, summarySchema, func(b []byte) error {
		var v struct {
			Summary string   `json:"summary"`
			Risks   []string `json:"risks"`
		}
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		if strings.TrimSpace(v.Summary) == "" {
			return errors.New("summary is empty")
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	var v struct {
		Summary string   `json:"summary"`
		Risks   []string `json:"risks"`
	}
	_ = json.Unmarshal(out, &v)
	return v.Summary, v.Risks, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return truncate(s, 200)
}
