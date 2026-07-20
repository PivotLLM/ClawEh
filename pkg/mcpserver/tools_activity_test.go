// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// collectOutbound subscribes to the bus and forwards outbound messages to a
// buffered channel for assertions.
func collectOutbound(t *testing.T, msgBus *bus.MessageBus) <-chan bus.OutboundMessage {
	t.Helper()
	collected := make(chan bus.OutboundMessage, 4)
	subCtx, subCancel := context.WithCancel(context.Background())
	t.Cleanup(subCancel)
	go func() {
		for {
			msg, ok := msgBus.SubscribeOutbound(subCtx)
			if !ok {
				return
			}
			collected <- msg
		}
	}()
	return collected
}

// TestDispatch_ToolActivityBreadcrumbPublished confirms that when a tool activity
// notifier returns a line and the session has a recorded channel + chatID, the MCP
// dispatch path publishes the breadcrumb to the originating user before the tool's
// own result — parity with the agent loop's "/tools on" breadcrumb.
func TestDispatch_ToolActivityBreadcrumbPublished(t *testing.T) {
	rf := &mockTool{
		name:   "write_file",
		params: map[string]any{},
		result: &tools.ToolResult{ForLLM: "wrote ok"},
	}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}

	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	st.SetSource("agent:alice:main", "slack", "C123")

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	collected := collectOutbound(t, msgBus)

	var gotAgentID, gotSessionKey, gotTool string
	notifier := func(agentID, sessionKey, toolName string, args map[string]any) string {
		gotAgentID, gotSessionKey, gotTool = agentID, sessionKey, toolName
		return "🔧 `write_file`"
	}

	out, isErr := dispatchToolCall(context.Background(), "write_file",
		map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, msgBus, notifier)
	if isErr {
		t.Fatalf("expected success, got error: %s", out)
	}

	// The notifier must receive the resolved identity, session key, and tool name.
	if gotAgentID != "alice" || gotSessionKey != "agent:alice:main" || gotTool != "write_file" {
		t.Errorf("notifier args = (%q,%q,%q); want (alice, agent:alice:main, write_file)",
			gotAgentID, gotSessionKey, gotTool)
	}

	select {
	case got := <-collected:
		if got.Channel != "slack" || got.ChatID != "C123" {
			t.Errorf("breadcrumb target = %s/%s; want slack/C123", got.Channel, got.ChatID)
		}
		if got.Content != "🔧 `write_file`" {
			t.Errorf("breadcrumb content = %q; want the notifier line", got.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected breadcrumb publish, none observed")
	}
}

// TestDispatch_ToolActivityOffPublishesNothing confirms that when the notifier
// returns "" (tool activity off for the session), no breadcrumb is published —
// the tool's ForLLM result is unaffected.
func TestDispatch_ToolActivityOffPublishesNothing(t *testing.T) {
	rf := &mockTool{
		name:   "write_file",
		params: map[string]any{},
		result: &tools.ToolResult{ForLLM: "wrote ok"},
	}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}

	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	st.SetSource("agent:alice:main", "slack", "C123")

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	collected := collectOutbound(t, msgBus)

	notifier := func(_, _, _ string, _ map[string]any) string { return "" }

	out, isErr := dispatchToolCall(context.Background(), "write_file",
		map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, msgBus, notifier)
	if isErr {
		t.Fatalf("expected success, got error: %s", out)
	}
	if out != "wrote ok" {
		t.Errorf("MCP response envelope must carry ForLLM, got %q", out)
	}

	select {
	case got := <-collected:
		t.Fatalf("expected no publish when notifier returns empty, got %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}
