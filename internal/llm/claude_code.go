package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

// ClaudeCode sends each request through the locally installed Claude Code CLI in
// headless mode (`claude -p`), signed in with the operator's own Claude
// subscription. Benchmark use only: it lets the benchmark compare Claude with
// other models without an API key.
//
// Claude Code runs with no tools of its own (--tools ""), no MCP servers, no
// settings, skills or saved sessions, in an empty working directory. Each call
// is one stateless invocation: the conversation is rendered as text and, when
// the request offers tools, the model answers with a structured "next action"
// (message + tool calls) that is turned back into tool_use blocks. Our own tool
// loop, gateway, budgets, DLP and ledger therefore apply exactly as for every
// other provider; Claude Code never touches the repository.
type ClaudeCode struct {
	name    string
	command string
	geo     string
	models  map[string]string
	// run executes the CLI; replaced in tests.
	run func(ctx context.Context, dir string, args []string, stdin []byte) ([]byte, error)
}

// NewClaudeCode builds the provider. command is the CLI to run ("claude" when empty).
func NewClaudeCode(name, command, geo string, models map[string]string) *ClaudeCode {
	if command == "" {
		command = "claude"
	}
	p := &ClaudeCode{name: name, command: command, geo: geo, models: models}
	p.run = p.exec
	return p
}

func (p *ClaudeCode) Name() string             { return p.name }
func (p *ClaudeCode) Model(role string) string { return p.models[role] }

// childEnv drops anything that would make Claude Code bill an API key or use
// another backend instead of the signed-in subscription.
func childEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "CLAUDE_CODE_") || k == "CLAUDECODE" {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func (p *ClaudeCode) exec(ctx context.Context, dir string, args []string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, p.command, args...)
	cmd.Dir = dir
	cmd.Env = childEnv()
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && len(bytes.TrimSpace(out)) == 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("claude code: %v: %s", err, truncate(stderr.String(), 300))
	}
	// A failed turn still prints its JSON result (is_error); let the caller read it.
	return out, nil
}

// ccResult is the part of `claude -p --output-format json` output we use.
type ccResult struct {
	IsError          bool            `json:"is_error"`
	APIErrorStatus   *int            `json:"api_error_status"`
	Subtype          string          `json:"subtype"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	Usage            struct {
		InputTokens              int64  `json:"input_tokens"`
		OutputTokens             int64  `json:"output_tokens"`
		CacheReadInputTokens     int64  `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64  `json:"cache_creation_input_tokens"`
		InferenceGeo             string `json:"inference_geo"`
	} `json:"usage"`
}

// action is the structured reply used in place of native tool calls.
type action struct {
	Message   string `json:"message"`
	ToolCalls []struct {
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"tool_calls"`
}

func actionSchema(names []string) map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"message", "tool_calls"},
		"properties": map[string]any{
			"message": map[string]any{"type": "string", "description": "Short note on what you are doing (may be empty)."},
			"tool_calls": map[string]any{
				"type": "array", "description": "Tool calls to make now, in order. Empty only when you have nothing left to do.",
				"items": map[string]any{
					"type": "object", "additionalProperties": false, "required": []any{"name", "input"},
					"properties": map[string]any{
						"name":  map[string]any{"type": "string", "enum": names},
						"input": map[string]any{"type": "object", "description": "Arguments matching that tool's input schema."},
					},
				},
			},
		},
	}
}

// render turns the request into a system prompt, a user prompt and (optionally)
// the JSON schema the reply must satisfy.
func renderClaudeCode(params anthropic.MessageNewParams) (system, prompt string, schema any, tools bool, err error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return "", "", nil, false, err
	}
	var req aRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", "", nil, false, err
	}
	var sys strings.Builder
	for _, s := range req.System {
		sys.WriteString(s.Text)
	}
	if len(req.Tools) > 0 {
		tools = true
		var names []string
		sys.WriteString("\n\n# Tools\nYou act only through these tools. Reply with your next action: a short message and the tool calls to make now. " +
			"Tool results come back in the next turn. Each tool's input must match its schema exactly.\n")
		for _, t := range req.Tools {
			names = append(names, t.Name)
			fmt.Fprintf(&sys, "\n## %s\n%s\nInput schema: %s\n", t.Name, t.Description, string(t.InputSchema))
		}
		schema = actionSchema(names)
	} else if f := req.OutputConfig.Format; f != nil && len(f.Schema) > 0 {
		schema = f.Schema
	}

	// A single user message is sent as is; a longer conversation is rendered as a transcript.
	if len(req.Messages) == 1 && req.Messages[0].Role == "user" {
		var b strings.Builder
		for _, bl := range req.Messages[0].Content {
			if bl.Type == "text" {
				b.WriteString(bl.Text)
			}
		}
		return sys.String(), b.String(), schema, tools, nil
	}
	var b strings.Builder
	b.WriteString("This is the conversation so far. Continue from the end of it.\n")
	for _, m := range req.Messages {
		for _, bl := range m.Content {
			switch bl.Type {
			case "text":
				if strings.TrimSpace(bl.Text) != "" {
					fmt.Fprintf(&b, "\n<%s>\n%s\n</%s>\n", m.Role, bl.Text, m.Role)
				}
			case "tool_use":
				fmt.Fprintf(&b, "\n<assistant_tool_call id=%q name=%q>\n%s\n</assistant_tool_call>\n", bl.ID, bl.Name, string(bl.Input))
			case "tool_result":
				status := "ok"
				if bl.IsError {
					status = "error"
				}
				fmt.Fprintf(&b, "\n<tool_result id=%q status=%q>\n%s\n</tool_result>\n", bl.ToolUseID, status, toolResultText(bl.Content))
			}
		}
	}
	if tools {
		b.WriteString("\nReply with your next action.")
	}
	return sys.String(), b.String(), schema, tools, nil
}

func (p *ClaudeCode) Send(ctx context.Context, call Call, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	system, prompt, schema, tools, err := renderClaudeCode(params)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "aegis-claude-code-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	sysFile := filepath.Join(dir, "system.md")
	if err := os.WriteFile(sysFile, []byte(system), 0o600); err != nil {
		return nil, err
	}
	args := []string{"-p", "--model", string(params.Model), "--output-format", "json",
		"--tools", "", "--strict-mcp-config", "--setting-sources", "", "--disable-slash-commands",
		"--no-session-persistence", "--exclude-dynamic-system-prompt-sections", "--system-prompt-file", sysFile}
	if e := string(params.OutputConfig.Effort); e != "" {
		args = append(args, "--effort", e)
	}
	if schema != nil {
		s, _ := json.Marshal(schema)
		args = append(args, "--json-schema", string(s))
	}
	out, err := p.run(ctx, dir, args, []byte(prompt))
	if err != nil {
		return nil, err
	}
	return claudeCodeMessage(out, string(params.Model), p.geo, tools, call)
}

// claudeCodeMessage converts the CLI's JSON result into an Anthropic Message.
func claudeCodeMessage(out []byte, model, geo string, tools bool, call Call) (*anthropic.Message, error) {
	var r ccResult
	if err := json.Unmarshal(bytes.TrimSpace(out), &r); err != nil {
		return nil, fmt.Errorf("claude code output: %w: %s", err, truncate(string(out), 200))
	}
	if r.IsError {
		status := 502 // unknown failures are treated as transient
		if r.APIErrorStatus != nil {
			status = *r.APIErrorStatus
		}
		low := strings.ToLower(r.Result)
		if strings.Contains(low, "log in") || strings.Contains(low, "login") || strings.Contains(low, "not authenticated") {
			status = 401
		}
		return nil, &HTTPError{StatusCode: status, Body: truncate(r.Subtype+": "+r.Result, 300)}
	}
	var content []map[string]any
	stop := "end_turn"
	switch {
	case tools:
		var a action
		if err := json.Unmarshal(r.StructuredOutput, &a); err != nil {
			return nil, fmt.Errorf("claude code action: %w", err)
		}
		if strings.TrimSpace(a.Message) != "" {
			content = append(content, map[string]any{"type": "text", "text": a.Message})
		}
		for i, tc := range a.ToolCalls {
			input := tc.Input
			if len(input) == 0 || string(input) == "null" {
				input = json.RawMessage(`{}`)
			}
			content = append(content, map[string]any{"type": "tool_use",
				"id":   fmt.Sprintf("toolu_cc_%d_%d_%d", call.Attempt, call.Seq, i),
				"name": tc.Name, "input": input})
		}
		if len(a.ToolCalls) > 0 {
			stop = "tool_use"
		}
	case len(r.StructuredOutput) > 0 && string(r.StructuredOutput) != "null":
		content = append(content, map[string]any{"type": "text", "text": string(r.StructuredOutput)})
	default:
		content = append(content, map[string]any{"type": "text", "text": r.Result})
	}
	if content == nil {
		content = []map[string]any{}
	}
	if r.Usage.InferenceGeo != "" {
		geo = r.Usage.InferenceGeo
	}
	msg := map[string]any{
		"id": fmt.Sprintf("cc_%d_%d", call.Attempt, call.Seq), "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": r.Usage.InputTokens, "output_tokens": r.Usage.OutputTokens,
			"cache_read_input_tokens": r.Usage.CacheReadInputTokens, "cache_creation_input_tokens": r.Usage.CacheCreationInputTokens,
			"inference_geo": geo},
	}
	data, _ := json.Marshal(msg)
	var m anthropic.Message
	if err := m.UnmarshalJSON(data); err != nil {
		return nil, err
	}
	return &m, nil
}
