package agent

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// mockDisplayTool returns a ToolResult with ForUser populated and Silent=false,
// modeling write_file/edit_file/append_file when display:true.
type mockDisplayTool struct {
	forUser string
}

func (m *mockDisplayTool) Name() string        { return "display_tool" }
func (m *mockDisplayTool) Description() string { return "Returns ForUser content" }
func (m *mockDisplayTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (m *mockDisplayTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	return &tools.ToolResult{
		ForLLM:  "ok",
		ForUser: m.forUser,
		Silent:  false,
	}
}

// TestRunLLMIteration_ToolForUser_PublishedOnInboundUserMessage verifies that
// when a tool returns ForUser content with Silent=false during the inbound
// user-message path (SendResponse=false), the agent loop publishes ForUser to
// the outbound bus.
//
// Regression: previously the sync publish branch was gated on opts.SendResponse,
// which is false for inbound user messages, silently dropping the ForUser block
// for write_file/edit_file/append_file with display:true.
func TestRunLLMIteration_ToolForUser_PublishedOnInboundUserMessage(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agentInstance := al.registry.GetDefaultAgent()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}

	const userPayload = "---\nfile contents preview\n---"
	agentInstance.Tools.Register(&mockDisplayTool{forUser: userPayload})
	// Allow the test tool through the per-agent allowlist that
	// ExecuteWithContext consults via the context-injected checker.
	if agentInstance.Config != nil {
		agentInstance.Config.Tools = []string{"*"}
	}

	agentInstance.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{
			{
				Content: "",
				ToolCalls: []providers.ToolCall{
					{
						ID:   "tc-1",
						Type: "function",
						Name: "display_tool",
						Function: &providers.FunctionCall{
							Name:      "display_tool",
							Arguments: `{}`,
						},
					},
				},
			},
			{Content: "all done"},
		},
		errors: []error{nil, nil},
	}

	// Collect outbound publishes concurrently so the publish path inside
	// runLLMIteration does not block on the bus buffer.
	collected := make(chan bus.OutboundMessage, 16)
	done := make(chan struct{})
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	go func() {
		defer close(done)
		for {
			msg, ok := msgBus.SubscribeOutbound(subCtx)
			if !ok {
				return
			}
			collected <- msg
		}
	}()

	messages := []providers.Message{{Role: "user", Content: "trigger display tool"}}
	opts := processOptions{
		SessionKey:   "tool-foruser-inbound",
		Channel:      "slack",
		ChatID:       "C123",
		UserMessage:  "trigger display tool",
		SendResponse: false,
	}

	cm, releaseCM := al.getContextManager(agentInstance, opts.SessionKey)
	defer releaseCM()

	if _, _, _, _, err := al.runLLMIteration(context.Background(), agentInstance, messages, opts, cm); err != nil {
		t.Fatalf("runLLMIteration: %v", err)
	}

	// Wait for the ForUser publish to be observed, with a short timeout to
	// keep the failure mode deterministic.
	var found bool
	var seen []bus.OutboundMessage
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case m := <-collected:
			seen = append(seen, m)
			if m.Content == userPayload && m.Channel == "slack" && m.ChatID == "C123" {
				found = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	subCancel()
	<-done

	if !found {
		t.Fatalf("expected ForUser payload published to outbound bus; got %d msgs: %+v", len(seen), seen)
	}
}
