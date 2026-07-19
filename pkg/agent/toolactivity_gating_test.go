package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// A tool call posts a "🔧 …" breadcrumb only when /tools is on for the session.
func TestToolActivity_BreadcrumbGating(t *testing.T) {
	run := func(t *testing.T, on bool) []string {
		t.Helper()
		al, cfg, msgBus, _, cleanup := newTestAgentLoop(t)
		defer cleanup()
		// Keep the model's ForUser streaming off so only the breadcrumb reaches the bus.
		cfg.Agents.Defaults.StreamToolActivity = false

		agentInstance := al.registry.GetDefaultAgent()
		if agentInstance == nil {
			t.Fatal("no default agent")
		}
		agentInstance.Tools.Register(&mockDisplayTool{forUser: "unused"})
		if agentInstance.Config != nil {
			agentInstance.Config.Tools = []string{"*"}
		}
		agentInstance.Provider = &sequenceProvider{
			responses: []*providers.LLMResponse{
				{Content: "", ToolCalls: []providers.ToolCall{{
					ID: "tc-1", Type: "function", Name: "display_tool",
					Function: &providers.FunctionCall{Name: "display_tool", Arguments: `{}`},
				}}},
				{Content: "done"},
			},
			errors: []error{nil, nil},
		}

		opts := processOptions{
			SessionKey:   "tools-gate",
			Channel:      "test",
			ChatID:       "chat",
			UserMessage:  "go",
			SendResponse: false,
		}
		al.setShowToolActivity(agentInstance, opts.SessionKey, on)

		collected := make(chan bus.OutboundMessage, 16)
		subCtx, subCancel := context.WithCancel(context.Background())
		defer subCancel()
		go func() {
			for {
				msg, ok := msgBus.SubscribeOutbound(subCtx)
				if !ok {
					return
				}
				collected <- msg
			}
		}()

		if _, err := al.runAgentLoop(context.Background(), agentInstance, opts); err != nil {
			t.Fatalf("runAgentLoop: %v", err)
		}

		var msgs []string
		deadline := time.After(2 * time.Second)
	drain:
		for {
			select {
			case m := <-collected:
				msgs = append(msgs, m.Content)
			case <-time.After(250 * time.Millisecond):
				break drain
			case <-deadline:
				break drain
			}
		}
		return msgs
	}

	hasBreadcrumb := func(msgs []string) bool {
		for _, m := range msgs {
			if strings.HasPrefix(m, "🔧 ") {
				return true
			}
		}
		return false
	}

	if !hasBreadcrumb(run(t, true)) {
		t.Fatal("toggle ON: expected a 🔧 breadcrumb, got none")
	}
	if hasBreadcrumb(run(t, false)) {
		t.Fatal("toggle OFF: breadcrumb published despite /tools off")
	}
}
