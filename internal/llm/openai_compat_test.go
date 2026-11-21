package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

func TestTranslateRequestToolLoop(t *testing.T) {
	tool := anthropic.ToolParam{Name: "read_file", InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{"path": map[string]any{"type": "string"}}}}
	params := anthropic.MessageNewParams{
		Model: "m", MaxTokens: 1000,
		System: []anthropic.TextBlockParam{{Text: "be careful"}},
		Tools:  []anthropic.ToolUnionParam{{OfTool: &tool}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("fix it")),
			anthropic.NewAssistantMessage(anthropic.NewToolUseBlock("call-1", map[string]any{"path": "a.py"}, "read_file")),
			anthropic.NewUserMessage(anthropic.NewToolResultBlock("call-1", "x = 1", false)),
		},
		OutputConfig: anthropic.OutputConfigParam{Effort: "xhigh"},
	}
	body, err := translateRequest(params)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	s := string(raw)
	for _, want := range []string{`"content":"be careful","role":"system"`, `"tool_calls":[{"function":{"arguments":"{\"path\":\"a.py\"}","name":"read_file"}`,
		`"content":"x = 1","role":"tool","tool_call_id":"call-1"`, `"reasoning_effort":"max"`, `"max_completion_tokens":1000`, `"type":"function"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestTranslateResponse(t *testing.T) {
	raw := `{"id":"x","model":"deepseek","choices":[{"finish_reason":"tool_calls","message":{"content":"",
	"tool_calls":[{"id":"call-9","function":{"name":"read_file","arguments":"{\"path\": \"app/main.py\"}"}}]}}],
	"usage":{"prompt_tokens":300,"completion_tokens":40,"prompt_tokens_details":{"cached_tokens":100}}}`
	m, err := translateResponse([]byte(raw), "US")
	if err != nil {
		t.Fatal(err)
	}
	if m.StopReason != anthropic.StopReasonToolUse || len(m.Content) != 1 {
		t.Fatalf("message = %+v", m)
	}
	tu, ok := m.Content[0].AsAny().(anthropic.ToolUseBlock)
	if !ok || tu.Name != "read_file" || !strings.Contains(tu.JSON.Input.Raw(), "app/main.py") {
		t.Fatalf("tool use = %+v", m.Content[0])
	}
	if m.Usage.InputTokens != 200 || m.Usage.CacheReadInputTokens != 100 || m.Usage.OutputTokens != 40 || m.Usage.InferenceGeo != "US" {
		t.Errorf("usage = %+v", m.Usage)
	}
	if m.RawJSON() == "" {
		t.Error("raw JSON missing (recording needs it)")
	}
}
