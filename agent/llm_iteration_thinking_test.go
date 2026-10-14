package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/PivotLLM/ClawEh/providers"
)

// TestRunLLMIteration_AnthropicThinkingReplayed verifies that the Anthropic
// thinking blocks returned with a tool call go back on the assistant message
// of the next request; Anthropic refuses a tool loop with thinking on without
// them.
func TestRunLLMIteration_AnthropicThinkingReplayed(t *testing.T) {
	al := newTestAgentLoop(t).al
	agentInstance := al.registry.Default()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	agentInstance.Tools.Register(&mockEchoTool{})
	if agentInstance.Config != nil {
		agentInstance.Config.Tools = []string{"*"}
	}

	thinking := []json.RawMessage{
		json.RawMessage(`{"type":"thinking","thinking":"plan","signature":"sig"}`),
		json.RawMessage(`{"type":"redacted_thinking","data":"opaque"}`),
	}
	provider := &sequenceProvider{
		responses: []*providers.LLMResponse{
			{
				AnthropicThinking: thinking,
				ToolCalls: []providers.ToolCall{{
					ID:   "tc-1",
					Type: "function",
					Name: "echo_tool",
					Function: &providers.FunctionCall{
						Name:      "echo_tool",
						Arguments: `{"text":"hi"}`,
					},
				}},
			},
			{Content: "done"},
		},
		errors: []error{nil, nil},
	}
	agentInstance.Provider = provider

	opts := processOptions{
		SessionKey:  "thinking-loop",
		Channel:     "cli",
		ChatID:      "direct",
		UserMessage: "go",
	}
	cm, releaseCM := al.getContextManager(agentInstance, opts.SessionKey)
	defer releaseCM()

	messages := []providers.Message{{Role: "user", Content: "go"}}
	if _, _, _, _, _, err := al.runLLMIteration(context.Background(), agentInstance, messages, opts, cm, nil); err != nil {
		t.Fatalf("runLLMIteration: %v", err)
	}

	provider.mu.Lock()
	sent := provider.lastMessages
	calls := provider.idx
	provider.mu.Unlock()
	if calls != 2 {
		t.Fatalf("provider called %d times, want 2", calls)
	}

	var found bool
	for _, m := range sent {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		found = true
		if len(m.AnthropicThinking) != len(thinking) {
			t.Fatalf("assistant tool-call message carries %d thinking blocks, want %d", len(m.AnthropicThinking), len(thinking))
		}
		for i := range thinking {
			if string(m.AnthropicThinking[i]) != string(thinking[i]) {
				t.Errorf("thinking block %d = %s, want %s", i, m.AnthropicThinking[i], thinking[i])
			}
		}
	}
	if !found {
		t.Fatal("second request has no assistant tool-call message")
	}
}
