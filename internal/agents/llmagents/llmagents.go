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
	"path/filepath"
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
	res, err := s.a.Router.Complete(ctx, llm.Call{TenantID: s.run.TenantID, RunID: s.run.RunID, Role: s.role,
		Attempt: s.run.Attempt, Seq: s.seq, Cassette: s.run.Scenario, PromptHash: s.a.prompts[s.role].hash,
		Redactions: s.redactions, Params: params})
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

func (a *Agents) importGraph(ctx context.Context, worktree, repoName string) string {
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

func (a *Agents) Develop(ctx context.Context, in agents.DevelopInput) (agents.DevelopOutput, error) {
	s := a.session(in.Run, "developer")
	var p plan
	if err := json.Unmarshal(in.Plan, &p); err != nil {
		return agents.DevelopOutput{}, err
	}
	gw := &gateway.Gateway{Root: in.Worktree, Protected: a.Cfg.Scope.ProtectedPaths,
		Planned: p.Scope.Planned, Adjacent: p.Scope.Adjacent, Writable: true,
		MaxRead: a.Cfg.Agents.MaxReadBytes, MaxCalls: a.Cfg.Agents.MaxToolCalls, MaxRunTests: a.Cfg.Agents.MaxRunTests,
		RunTests: a.runTests(in.Worktree),
		Audit: func(ctx context.Context, c gateway.Call) {
			if a.Audit != nil {
				a.Audit(ctx, ledger.ToolCall{TenantID: in.TenantID, RunID: in.RunID, Role: "developer", Attempt: in.Attempt,
					Tool: c.Tool, ArgsHash: c.ArgsHash, Allowed: c.Allowed, Reason: c.Reason, DurationMS: c.DurationMS})
			}
		}}
	brief := fence("specification", string(in.RequirementSpec)) + fence("plan", string(in.Plan))
	if in.Diagnostics != "" {
		brief += fence("previous_attempt_failures", in.Diagnostics) +
			"Your previous attempt is already in the worktree. Fix exactly these failures.\n"
	}
	brief += "Implement the plan. Start by reading the planned files."
	msgs := []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(s.clean(brief)))}
	tools := gw.Tools()
	nudged := false
	for turn := 0; turn < a.Cfg.Agents.MaxToolCalls+4; turn++ {
		resp, err := s.call(ctx, anthropic.MessageNewParams{System: s.system(), Messages: msgs, Tools: tools,
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
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(
				"Continue working with the tools. When the change is complete and tested, call finish.")))
			continue
		}
		var results []anthropic.ContentBlockParamUnion
		var done *gateway.Outcome
		for _, block := range resp.Content {
			tu, ok := block.AsAny().(anthropic.ToolUseBlock)
			if !ok {
				continue
			}
			out := gw.Execute(ctx, tu.Name, json.RawMessage(tu.JSON.Input.Raw()))
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
