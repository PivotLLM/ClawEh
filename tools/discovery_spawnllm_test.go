// ClawEh
// License: MIT

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/PivotLLM/spawnllm/common"
	"github.com/PivotLLM/spawnllm/openai_compat"

	"github.com/PivotLLM/ClawEh/providers"
)

// spawnllmRegistry is a discovery-on registry with core tools, the two
// discovery tools and three hidden upstream tools.
func spawnllmRegistry() (*ToolRegistry, *SearchTool) {
	r := NewToolRegistry()
	r.Register(newMockTool("file_read", "Read a file"))
	r.Register(newMockTool("time_now", "Current time"))
	search := NewSearchTool(r, 5)
	r.RegisterSuite(search)
	r.RegisterSuite(NewToolDetailsTool(r))
	r.RegisterHidden(newMockTool("mcp_x_read", "Read a record from X"))
	r.RegisterHidden(newMockTool("mcp_x_write", "Write a record to X"))
	r.RegisterHidden(newMockTool("mcp_bob_fetch", "Fetch a page for Bob"))
	return r, search
}

var spawnllmCoreTools = []string{"file_read", "get_tool_details", "search_tools", "time_now"}

// history is a conversation whose last message is a tool result carrying refs.
func history(refs []string) []providers.Message {
	return []providers.Message{
		{Role: "user", Content: "read the X record for Alice"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: "c1", Type: "function", Name: "search_tools",
			Function: &providers.FunctionCall{Name: "search_tools", Arguments: `{"query":"x read"}`},
		}}},
		{Role: "tool", ToolCallID: "c1", Content: "search result", ToolReferences: refs},
	}
}

// searchReferences runs search_tools for exactly mcp_x_read and returns the
// references it produced.
func searchReferences(t *testing.T, search *SearchTool) []string {
	t.Helper()
	res := search.Execute(context.Background(), map[string]any{"query": "^mcp_x_read$", "regex": true})
	if res.IsError {
		t.Fatalf("search_tools: %s", res.ForLLM)
	}
	if want := []string{"mcp_x_read"}; !slices.Equal(res.ToolReferences, want) {
		t.Fatalf("search references = %v, want %v", res.ToolReferences, want)
	}
	return res.ToolReferences
}

// For a provider without native deferred tools, spawnllm's request builder
// turns ClawEh's flagged list into the core tools plus the referenced hidden
// tool, with no other hidden tool; without references, only the core tools.
func TestSpawnllmRequestTools_ClawEhDefinitions(t *testing.T) {
	r, search := spawnllmRegistry()
	defs := r.ToProviderDefs()
	refs := searchReferences(t, search)

	tests := []struct {
		name string
		msgs []providers.Message
		want []string
	}{
		{"referenced", history(refs), append(slices.Clone(spawnllmCoreTools), "mcp_x_read")},
		{"no references", history(nil), spawnllmCoreTools},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := common.RequestTools(defs, tt.msgs)
			if names := defNames(got); !slices.Equal(names, tt.want) {
				t.Errorf("request tools = %v, want %v", names, tt.want)
			}
			for _, d := range got {
				if d.Deferred {
					t.Errorf("%s is still marked deferred", d.Function.Name)
				}
			}
		})
	}
}

// The same through spawnllm's Chat Completions provider against a local
// server: the request body's tool list and the search result's text.
func TestSpawnllmOpenAICompat_ClawEhDefinitions(t *testing.T) {
	// chatBody is the part of a Chat Completions request these checks read.
	type chatBody struct {
		Tools []struct {
			Function map[string]json.RawMessage `json:"function"`
		} `json:"tools"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	var (
		mu   sync.Mutex
		body chatBody
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body = chatBody{}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer server.Close()

	r, search := spawnllmRegistry()
	defs := r.ToProviderDefs()
	refs := searchReferences(t, search)
	p := openai_compat.NewProvider("test-key", server.URL, "")

	tests := []struct {
		name     string
		msgs     []providers.Message
		want     []string
		toolText string
	}{
		{"referenced", history(refs), append(slices.Clone(spawnllmCoreTools), "mcp_x_read"), "search result\n\nTools found: mcp_x_read"},
		{"no references", history(nil), spawnllmCoreTools, "search result"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := p.Chat(t.Context(), tt.msgs, defs, "gpt-4o", nil); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			names := make([]string, 0, len(body.Tools))
			for _, item := range body.Tools {
				var name string
				if err := json.Unmarshal(item.Function["name"], &name); err != nil {
					t.Fatalf("tool name: %v", err)
				}
				names = append(names, name)
				if _, ok := item.Function["defer_loading"]; ok {
					t.Errorf("%s carries defer_loading on a non-native provider", name)
				}
			}
			if !slices.Equal(names, tt.want) {
				t.Errorf("request tools = %v, want %v", names, tt.want)
			}
			var toolText string
			for _, m := range body.Messages {
				if m.Role == "tool" {
					if err := json.Unmarshal(m.Content, &toolText); err != nil {
						t.Fatalf("tool message content: %v", err)
					}
				}
			}
			if toolText != tt.toolText {
				t.Errorf("tool result text = %q, want %q", toolText, tt.toolText)
			}
			if strings.Contains(toolText, "mcp_bob_fetch") {
				t.Errorf("unreferenced hidden tool named in %q", toolText)
			}
		})
	}
}
