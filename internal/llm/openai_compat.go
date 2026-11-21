package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

// HTTPError is a non-2xx response from an OpenAI-compatible endpoint.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body) }

// OpenAICompat sends requests to an OpenAI-compatible chat-completions endpoint
// (used to benchmark open-weights models). It translates our request format to
// theirs and the response back, so the router, gateway and ledger are unchanged.
type OpenAICompat struct {
	name    string
	baseURL string
	apiKey  string
	geo     string
	models  map[string]string
	client  *http.Client
}

// NewOpenAICompat builds the provider; the key is read from a secret reference.
func NewOpenAICompat(name, baseURL, apiKey, geo string, models map[string]string) *OpenAICompat {
	return &OpenAICompat{name: name, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, geo: geo,
		models: models, client: &http.Client{}}
}

func (p *OpenAICompat) Name() string             { return p.name }
func (p *OpenAICompat) Model(role string) string { return p.models[role] }

// wire types for the Anthropic-format request, read from its JSON encoding.
type aBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type aRequest struct {
	Model     string `json:"model"`
	MaxTokens int64  `json:"max_tokens"`
	System    []struct {
		Text string `json:"text"`
	} `json:"system"`
	Messages []struct {
		Role    string   `json:"role"`
		Content []aBlock `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	} `json:"tools"`
	OutputConfig struct {
		Effort string `json:"effort"`
		Format *struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"format"`
	} `json:"output_config"`
}

func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []aBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, bl := range blocks {
			b.WriteString(bl.Text)
		}
		return b.String()
	}
	return string(raw)
}

// effortMap maps our effort levels onto the endpoint's reasoning_effort values.
var effortMap = map[string]string{"low": "low", "medium": "low", "high": "high", "xhigh": "max", "max": "max"}

// translateRequest converts an Anthropic-format request into an OpenAI-format body.
func translateRequest(params anthropic.MessageNewParams) (map[string]any, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var req aRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	var msgs []map[string]any
	var sys strings.Builder
	for _, s := range req.System {
		sys.WriteString(s.Text)
	}
	if sys.Len() > 0 {
		msgs = append(msgs, map[string]any{"role": "system", "content": sys.String()})
	}
	for _, m := range req.Messages {
		var text strings.Builder
		var calls []map[string]any
		var toolMsgs []map[string]any
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				text.WriteString(b.Text)
			case "tool_use":
				args := string(b.Input)
				if args == "" {
					args = "{}"
				}
				calls = append(calls, map[string]any{"id": b.ID, "type": "function",
					"function": map[string]any{"name": b.Name, "arguments": args}})
			case "tool_result":
				content := toolResultText(b.Content)
				if b.IsError {
					content = "ERROR: " + content
				}
				toolMsgs = append(toolMsgs, map[string]any{"role": "tool", "tool_call_id": b.ToolUseID, "content": content})
			}
			// thinking blocks are model-specific and are not forwarded
		}
		if m.Role == "assistant" {
			am := map[string]any{"role": "assistant", "content": text.String()}
			if len(calls) > 0 {
				am["tool_calls"] = calls
			}
			msgs = append(msgs, am)
			continue
		}
		// Tool results must directly follow the assistant turn that asked for them.
		msgs = append(msgs, toolMsgs...)
		if text.Len() > 0 {
			msgs = append(msgs, map[string]any{"role": "user", "content": text.String()})
		}
	}
	body := map[string]any{"model": req.Model, "messages": msgs, "stream": false}
	if req.MaxTokens > 0 {
		body["max_completion_tokens"] = req.MaxTokens
	}
	if e, ok := effortMap[req.OutputConfig.Effort]; ok {
		body["reasoning_effort"] = e
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.InputSchema}})
		}
		body["tools"] = tools
	}
	if f := req.OutputConfig.Format; f != nil && len(f.Schema) > 0 {
		body["response_format"] = map[string]any{"type": "json_schema",
			"json_schema": map[string]any{"name": "output", "strict": true, "schema": f.Schema}}
	}
	return body, nil
}

type oResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

var stopMap = map[string]string{"stop": "end_turn", "tool_calls": "tool_use", "length": "max_tokens", "content_filter": "refusal"}

// translateResponse converts an OpenAI-format response into an Anthropic Message.
func translateResponse(raw []byte, geo string) (*anthropic.Message, error) {
	var r oResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("response: %w", err)
	}
	if len(r.Choices) == 0 {
		return nil, fmt.Errorf("response has no choices")
	}
	c := r.Choices[0]
	var content []map[string]any
	if strings.TrimSpace(c.Message.Content) != "" {
		content = append(content, map[string]any{"type": "text", "text": c.Message.Content})
	}
	for _, tc := range c.Message.ToolCalls {
		var input any = map[string]any{}
		if json.Unmarshal([]byte(tc.Function.Arguments), &input) != nil {
			input = map[string]any{} // the gateway reports the missing fields back to the model
		}
		content = append(content, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input})
	}
	if content == nil {
		content = []map[string]any{}
	}
	stop := stopMap[c.FinishReason]
	if stop == "" {
		stop = "end_turn"
	}
	cached := r.Usage.PromptTokensDetails.CachedTokens
	msg := map[string]any{
		"id": r.ID, "type": "message", "role": "assistant", "model": r.Model, "content": content,
		"stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": r.Usage.PromptTokens - cached, "output_tokens": r.Usage.CompletionTokens,
			"cache_read_input_tokens": cached, "cache_creation_input_tokens": 0, "inference_geo": geo},
	}
	data, _ := json.Marshal(msg)
	var m anthropic.Message
	if err := m.UnmarshalJSON(data); err != nil {
		return nil, err
	}
	return &m, nil
}

func (p *OpenAICompat) Send(ctx context.Context, _ Call, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	body, err := translateRequest(params)
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: truncate(string(raw), 300)}
	}
	return translateResponse(raw, p.geo)
}
