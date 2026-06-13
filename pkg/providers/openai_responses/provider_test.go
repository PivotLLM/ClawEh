// ClawEh
// License: MIT

package openai_responses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/providers/protocoltypes"
)

// captureServer returns an httptest server that records the last request body
// and replies with the given JSON, plus the constructed provider pointed at it.
func captureServer(t *testing.T, reply string, opts ...Option) (*Provider, *map[string]any) {
	t.Helper()
	captured := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want /responses", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	p := NewProvider("k", srv.URL, "", opts...)
	return p, &captured
}

func TestChat_TextResponse(t *testing.T) {
	reply := `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello world"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`
	p, body := captureServer(t, reply)

	msgs := []Message{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "hi"},
	}
	out, err := p.Chat(context.Background(), msgs, nil, "gpt-5", map[string]any{"max_tokens": 256})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if out.Content != "hello world" {
		t.Errorf("Content = %q", out.Content)
	}
	if out.FinishReason != "stop" || !out.Normal {
		t.Errorf("finish=%q normal=%v", out.FinishReason, out.Normal)
	}
	if out.Usage == nil || out.Usage.PromptTokens != 10 || out.Usage.CompletionTokens != 5 {
		t.Errorf("usage = %+v", out.Usage)
	}

	// Request mapping: system hoisted to instructions, user in input, max_output_tokens set.
	if (*body)["instructions"] != "be brief" {
		t.Errorf("instructions = %v", (*body)["instructions"])
	}
	if _, ok := (*body)["messages"]; ok {
		t.Error("must not send chat-style 'messages'")
	}
	input, _ := (*body)["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1 (user only)", len(input))
	}
	first, _ := input[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "hi" {
		t.Errorf("input[0] = %v", first)
	}
	if mot, _ := (*body)["max_output_tokens"].(float64); mot != 256 {
		t.Errorf("max_output_tokens = %v, want 256", (*body)["max_output_tokens"])
	}
}

func TestChat_ToolCallResponse(t *testing.T) {
	reply := `{"status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}]}`
	p, body := captureServer(t, reply)

	tools := []ToolDefinition{{
		Type: "function",
		Function: protocoltypes.ToolFunctionDefinition{
			Name:        "get_weather",
			Description: "weather",
			Parameters:  map[string]any{"type": "object"},
		},
	}}
	out, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "weather?"}}, tools, "gpt-5", nil)
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if out.FinishReason != "tool_calls" {
		t.Errorf("finish = %q, want tool_calls", out.FinishReason)
	}
	if len(out.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d", len(out.ToolCalls))
	}
	tc := out.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function == nil || tc.Function.Name != "get_weather" || tc.Function.Arguments != `{"city":"NYC"}` {
		t.Errorf("tool call = %+v / fn=%+v", tc, tc.Function)
	}

	// Tools flattened to Responses shape (no nested "function" wrapper).
	toolsBody, _ := (*body)["tools"].([]any)
	if len(toolsBody) != 1 {
		t.Fatalf("tools len = %d", len(toolsBody))
	}
	td, _ := toolsBody[0].(map[string]any)
	if td["type"] != "function" || td["name"] != "get_weather" {
		t.Errorf("tool def = %v", td)
	}
	if _, nested := td["function"]; nested {
		t.Error("tool def must be flattened (no nested 'function')")
	}
}

func TestChat_ToolHistoryReplay(t *testing.T) {
	reply := `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`
	p, body := captureServer(t, reply)

	msgs := []Message{
		{Role: "user", Content: "weather?"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: &FunctionCall{Name: "get_weather", Arguments: `{"city":"NYC"}`}}}},
		{Role: "tool", ToolCallID: "call_1", Content: "sunny"},
	}
	if _, err := p.Chat(context.Background(), msgs, nil, "gpt-5", nil); err != nil {
		t.Fatalf("Chat error: %v", err)
	}

	input, _ := (*body)["input"].([]any)
	// user message, function_call item, function_call_output item
	if len(input) != 3 {
		t.Fatalf("input len = %d, want 3; got %v", len(input), input)
	}
	fc, _ := input[1].(map[string]any)
	if fc["type"] != "function_call" || fc["call_id"] != "call_1" || fc["name"] != "get_weather" {
		t.Errorf("function_call item = %v", fc)
	}
	fco, _ := input[2].(map[string]any)
	if fco["type"] != "function_call_output" || fco["call_id"] != "call_1" || fco["output"] != "sunny" {
		t.Errorf("function_call_output item = %v", fco)
	}
}

func TestChat_JSONObjectFormat(t *testing.T) {
	reply := `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{}"}]}]}`
	// protocol "openai-responses" is JSON-capable by default.
	p, body := captureServer(t, reply, WithProtocol("openai-responses"))

	_, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, "gpt-5",
		map[string]any{ResponseFormatJSONObjectOption: true})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	text, _ := (*body)["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if format["type"] != "json_object" {
		t.Errorf("text.format = %v, want json_object", text)
	}
	if _, ok := (*body)["response_format"]; ok {
		t.Error("must not send chat-style top-level 'response_format'")
	}
}

func TestChat_JSONObjectDroppedWhenNotCapable(t *testing.T) {
	reply := `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{}"}]}]}`
	p, body := captureServer(t, reply, WithProtocol("xai")) // not capable, not opted in

	if _, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, "grok",
		map[string]any{ResponseFormatJSONObjectOption: true}); err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if _, ok := (*body)["text"]; ok {
		t.Error("text.format must be dropped when protocol is not JSON-capable")
	}
}

func TestChat_ReasoningEffort(t *testing.T) {
	reply := `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`
	p, body := captureServer(t, reply, WithReasoningEffort("high"))

	if _, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, "gpt-5", nil); err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	reasoning, _ := (*body)["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Errorf("reasoning = %v, want effort=high", reasoning)
	}
}

func TestChat_IncompleteIsLength(t *testing.T) {
	reply := `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}]}`
	p, _ := captureServer(t, reply)

	out, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, "gpt-5", nil)
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if out.FinishReason != "length" || out.Normal {
		t.Errorf("finish=%q normal=%v, want length/false", out.FinishReason, out.Normal)
	}
	if out.Content != "partial" {
		t.Errorf("content = %q", out.Content)
	}
}

func TestChat_DropParams(t *testing.T) {
	reply := `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`
	p, body := captureServer(t, reply, WithDropParams([]string{"temperature"}))

	if _, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, "gpt-5",
		map[string]any{"temperature": 0.7}); err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if _, ok := (*body)["temperature"]; ok {
		t.Error("temperature should have been dropped by drop_params")
	}
}

func TestChat_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad"}}`))
	}))
	defer srv.Close()
	p := NewProvider("k", srv.URL, "")

	out, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil, "gpt-5", nil)
	if err == nil {
		t.Fatal("expected error on non-200")
	}
	if out == nil || out.Status == nil || out.Status.Success {
		t.Errorf("expected failed dispatch status, got %+v", out)
	}
}
