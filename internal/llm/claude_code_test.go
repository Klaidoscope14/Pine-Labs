package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

func toolLoopParams() anthropic.MessageNewParams {
	tool := anthropic.ToolParam{Name: "read_file", InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{"path": map[string]any{"type": "string"}}}}
	return anthropic.MessageNewParams{
		Model: "claude-sonnet-5-5", MaxTokens: 1000,
		System: []anthropic.TextBlockParam{{Text: "be careful"}},
		Tools:  []anthropic.ToolUnionParam{{OfTool: &tool}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("fix it")),
			anthropic.NewAssistantMessage(anthropic.NewToolUseBlock("call-1", map[string]any{"path": "a.py"}, "read_file")),
			anthropic.NewUserMessage(anthropic.NewToolResultBlock("call-1", "x = 1", false)),
		},
		OutputConfig: anthropic.OutputConfigParam{Effort: "high"},
	}
}

func TestClaudeCodeToolLoop(t *testing.T) {
	p := NewClaudeCode("cc", "", "US", nil)
	var gotArgs []string
	var gotPrompt, gotSystem string
	p.run = func(_ context.Context, dir string, args []string, stdin []byte) ([]byte, error) {
		gotArgs, gotPrompt = args, string(stdin)
		i := slices.Index(args, "--system-prompt-file")
		sys, _ := os.ReadFile(args[i+1])
		gotSystem = string(sys)
		return []byte(`{"type":"result","is_error":false,"result":"","structured_output":{"message":"reading","tool_calls":[{"name":"read_file","input":{"path":"b.py"}}]},
			"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40,"inference_geo":"global"}}`), nil
	}
	msg, err := p.Send(context.Background(), Call{Attempt: 1, Seq: 3}, toolLoopParams())
	if err != nil {
		t.Fatal(err)
	}
	// Claude Code gets no tools of its own and no ambient configuration.
	for _, want := range []string{"--tools", "--strict-mcp-config", "--setting-sources", "--no-session-persistence", "--json-schema"} {
		if !slices.Contains(gotArgs, want) {
			t.Errorf("missing %s in %v", want, gotArgs)
		}
	}
	if i := slices.Index(gotArgs, "--tools"); gotArgs[i+1] != "" {
		t.Errorf("--tools must be empty, got %q", gotArgs[i+1])
	}
	if i := slices.Index(gotArgs, "--effort"); i < 0 || gotArgs[i+1] != "high" {
		t.Errorf("effort not passed: %v", gotArgs)
	}
	if !strings.Contains(gotSystem, "be careful") || !strings.Contains(gotSystem, "## read_file") {
		t.Errorf("system prompt missing instructions or tools: %q", gotSystem)
	}
	if !strings.Contains(gotPrompt, `<assistant_tool_call id="call-1" name="read_file">`) || !strings.Contains(gotPrompt, "x = 1") {
		t.Errorf("transcript missing tool turn: %q", gotPrompt)
	}
	if msg.StopReason != anthropic.StopReasonToolUse {
		t.Fatalf("stop reason %q", msg.StopReason)
	}
	var tu anthropic.ToolUseBlock
	for _, b := range msg.Content {
		if x, ok := b.AsAny().(anthropic.ToolUseBlock); ok {
			tu = x
		}
	}
	if tu.Name != "read_file" || tu.ID != "toolu_cc_1_3_0" || !strings.Contains(tu.JSON.Input.Raw(), "b.py") {
		t.Errorf("tool use: %+v", tu)
	}
	if msg.Usage.OutputTokens != 20 || msg.Usage.CacheCreationInputTokens != 40 || msg.Usage.InferenceGeo != "global" {
		t.Errorf("usage: %+v", msg.Usage)
	}
}

func TestClaudeCodeStructuredAndText(t *testing.T) {
	p := NewClaudeCode("cc", "", "US", nil)
	p.run = func(_ context.Context, _ string, args []string, stdin []byte) ([]byte, error) {
		if slices.Contains(args, "--json-schema") {
			if string(stdin) != "analyse this" {
				t.Errorf("single user message should be sent as is, got %q", stdin)
			}
			return []byte(`{"is_error":false,"structured_output":{"ok":true},"usage":{}}`), nil
		}
		return []byte(`{"is_error":false,"result":"hello","usage":{}}`), nil
	}
	params := anthropic.MessageNewParams{Model: "m",
		Messages:     []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("analyse this"))},
		OutputConfig: anthropic.OutputConfigParam{Format: anthropic.JSONOutputFormatParam{Schema: map[string]any{"type": "object"}}}}
	msg, err := p.Send(context.Background(), Call{}, params)
	if err != nil {
		t.Fatal(err)
	}
	if got := text(msg); got != `{"ok":true}` {
		t.Errorf("structured text = %q", got)
	}
	params.OutputConfig = anthropic.OutputConfigParam{}
	msg, err = p.Send(context.Background(), Call{}, params)
	if err != nil || text(msg) != "hello" || msg.StopReason != anthropic.StopReasonEndTurn {
		t.Errorf("plain text: %v %+v", err, msg)
	}
}

func text(m *anthropic.Message) string {
	var b strings.Builder
	for _, block := range m.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

func TestClaudeCodeErrors(t *testing.T) {
	cases := map[string]struct {
		out       string
		status    int
		retryable bool
	}{
		"rate limit": {`{"is_error":true,"api_error_status":429,"subtype":"error","result":"rate limited"}`, 429, true},
		"not signed": {`{"is_error":true,"subtype":"error","result":"Not logged in · Please run /login"}`, 401, false},
		"unknown":    {`{"is_error":true,"subtype":"error_during_execution","result":"boom"}`, 502, true},
	}
	for name, c := range cases {
		_, err := claudeCodeMessage([]byte(c.out), "m", "US", false, Call{})
		var herr *HTTPError
		if !errors.As(err, &herr) || herr.StatusCode != c.status {
			t.Errorf("%s: got %v, want status %d", name, err, c.status)
			continue
		}
		if retryable(err) != c.retryable || accessDenied(err) != (c.status == 401) {
			t.Errorf("%s: retryable=%v accessDenied=%v", name, retryable(err), accessDenied(err))
		}
	}
}

func TestChildEnvDropsAPIKeys(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	for _, kv := range childEnv() {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") || strings.HasPrefix(kv, "CLAUDE_CODE_USE_BEDROCK=") {
			t.Errorf("leaked %s", kv)
		}
	}
	_ = json.Valid
}

// TestClaudeCodeLive makes one real tool-loop call. Opt in: AEGIS_LIVE_CLAUDE_CODE=1.
func TestClaudeCodeLive(t *testing.T) {
	if os.Getenv("AEGIS_LIVE_CLAUDE_CODE") != "1" {
		t.Skip("set AEGIS_LIVE_CLAUDE_CODE=1 to call the real Claude Code CLI")
	}
	p := NewClaudeCode("cc", "", "US", nil)
	msg, err := p.Send(context.Background(), Call{Attempt: 1}, toolLoopParams())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(msg)
	t.Logf("%s", raw)
	if msg.StopReason != anthropic.StopReasonToolUse && msg.StopReason != anthropic.StopReasonEndTurn {
		t.Fatalf("stop reason %q", msg.StopReason)
	}
}

type slowProvider struct {
	mu          sync.Mutex
	running, pk int
}

func (s *slowProvider) Name() string        { return "slow" }
func (s *slowProvider) Model(string) string { return "m" }
func (s *slowProvider) Send(context.Context, Call, anthropic.MessageNewParams) (*anthropic.Message, error) {
	s.mu.Lock()
	s.running++
	if s.running > s.pk {
		s.pk = s.running
	}
	s.mu.Unlock()
	time.Sleep(15 * time.Millisecond)
	s.mu.Lock()
	s.running--
	s.mu.Unlock()
	return &anthropic.Message{}, nil
}

func TestLimitedCapsInFlight(t *testing.T) {
	inner := &slowProvider{}
	l := &Limited{Inner: inner, Slots: make(chan struct{}, 2)}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = l.Send(context.Background(), Call{}, anthropic.MessageNewParams{}) }()
	}
	wg.Wait()
	if inner.pk > 2 {
		t.Fatalf("peak in flight %d, cap 2", inner.pk)
	}
}
