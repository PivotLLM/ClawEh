// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/tools"
)

// TestDispatch_ForUserPublishedToOriginatingChannel confirms that when a tool
// returns ForUser content (Silent=false) and a turn runs on the token, the MCP
// dispatch path publishes ForUser to the message bus targeted at that turn's
// chat — as the agent loop does.
func TestDispatch_ForUserPublishedToOriginatingChannel(t *testing.T) {
	const userPayload = "---\nfile contents preview\n---"

	rf := &mockTool{
		name:   "write_file",
		params: map[string]any{},
		result: &tools.ToolResult{
			ForLLM:  "wrote ok",
			ForUser: userPayload,
			Silent:  false,
		},
	}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}

	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	defer st.BeginTurn("agent:alice:main", "slack", "C123")()

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	collected := make(chan bus.OutboundMessage, 4)
	subCtx := t.Context()
	go func() {
		for {
			msg, ok := msgBus.SubscribeOutbound(subCtx)
			if !ok {
				return
			}
			collected <- msg
		}
	}()

	out, isErr := dispatchToolCall(context.Background(), "write_file",
		map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, msgBus, nil)
	if isErr {
		t.Fatalf("expected success, got error: %s", out)
	}
	if out != "wrote ok" {
		t.Errorf("MCP response envelope must carry only ForLLM, got %q", out)
	}
	if strings.Contains(out, userPayload) {
		t.Errorf("ForUser payload leaked into MCP response envelope: %q", out)
	}

	select {
	case got := <-collected:
		if got.Channel != "slack" {
			t.Errorf("expected channel=slack, got %q", got.Channel)
		}
		if got.ChatID != "C123" {
			t.Errorf("expected chatID=C123, got %q", got.ChatID)
		}
		if got.Content != userPayload {
			t.Errorf("expected content=%q, got %q", userPayload, got.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected outbound publish of ForUser payload, none observed")
	}
}

// TestDispatch_SilentToolResultIsNotPublished confirms that when a tool sets
// Silent=true on the result, no ForUser publish happens even if ForUser is
// non-empty. The MCP response continues to return ForLLM unchanged.
func TestDispatch_SilentToolResultIsNotPublished(t *testing.T) {
	rf := &mockTool{
		name:   "write_file",
		params: map[string]any{},
		result: &tools.ToolResult{
			ForLLM:  "wrote ok",
			ForUser: "should not be sent",
			Silent:  true,
		},
	}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}

	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	defer st.BeginTurn("agent:alice:main", "slack", "C123")()

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	collected := make(chan bus.OutboundMessage, 4)
	subCtx := t.Context()
	go func() {
		for {
			msg, ok := msgBus.SubscribeOutbound(subCtx)
			if !ok {
				return
			}
			collected <- msg
		}
	}()

	out, isErr := dispatchToolCall(context.Background(), "write_file",
		map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, msgBus, nil)
	if isErr {
		t.Fatalf("expected success, got error: %s", out)
	}
	if out != "wrote ok" {
		t.Errorf("MCP response envelope must carry ForLLM, got %q", out)
	}

	select {
	case got := <-collected:
		t.Fatalf("expected no publish when Silent=true, got %+v", got)
	case <-time.After(150 * time.Millisecond):
		// expected: nothing published
	}
}

// TestDispatch_ForUserDroppedWhenNoActiveChannel confirms that when no turn
// runs on the token's session (a call outside any turn) and a tool returns
// ForUser, the MCP
// dispatch path silently drops the publish: no error to the caller, no
// injection into ForLLM, and an info-level log line is emitted.
func TestDispatch_ForUserDroppedWhenNoActiveChannel(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	const userPayload = "---\nfile contents preview\n---"

	rf := &mockTool{
		name:   "write_file",
		params: map[string]any{},
		result: &tools.ToolResult{
			ForLLM:  "wrote ok",
			ForUser: userPayload,
			Silent:  false,
		},
	}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}

	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	// Deliberately no turn running: the call has no chat.

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	collected := make(chan bus.OutboundMessage, 4)
	subCtx := t.Context()
	go func() {
		for {
			msg, ok := msgBus.SubscribeOutbound(subCtx)
			if !ok {
				return
			}
			collected <- msg
		}
	}()

	out, isErr := dispatchToolCall(context.Background(), "write_file",
		map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, msgBus, nil)
	if isErr {
		t.Fatalf("expected success despite missing channel, got error: %s", out)
	}
	if out != "wrote ok" {
		t.Errorf("ForLLM must be unaffected; expected %q, got %q", "wrote ok", out)
	}
	if strings.Contains(out, userPayload) {
		t.Errorf("ForUser must not be injected into ForLLM, got: %q", out)
	}

	select {
	case got := <-collected:
		t.Fatalf("expected no publish when no turn runs, got %+v", got)
	case <-time.After(150 * time.Millisecond):
		// expected
	}

	logs := buf.String()
	if !strings.Contains(logs, `"message":"mcp.foruser.dropped"`) {
		t.Errorf("expected mcp.foruser.dropped log line, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"reason":"no_turn_running"`) {
		t.Errorf("expected reason=no_turn_running, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"tool":"write_file"`) {
		t.Errorf("expected tool field in dropped log, got:\n%s", logs)
	}
}

// TestDispatch_MCPResponseContainsOnlyForLLM is a focused regression that
// confirms even when ForUser is present and the publish path runs, the MCP
// response envelope returned to the caller contains only ForLLM. ForUser is
// strictly a side-channel publish; it must never appear in the JSON-RPC
// tool response delivered to the MCP client.
func TestDispatch_MCPResponseContainsOnlyForLLM(t *testing.T) {
	const llmPayload = "wrote 42 bytes"
	const userPayload = "---\nfile contents preview\n---"

	rf := &mockTool{
		name:   "write_file",
		params: map[string]any{},
		result: &tools.ToolResult{
			ForLLM:  llmPayload,
			ForUser: userPayload,
			Silent:  false,
		},
	}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}

	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	defer st.BeginTurn("agent:alice:main", "slack", "C123")()

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	// Drain outbound so the publish does not stall the test.
	subCtx := t.Context()
	go func() {
		for {
			if _, ok := msgBus.SubscribeOutbound(subCtx); !ok {
				return
			}
		}
	}()

	out, isErr := dispatchToolCall(context.Background(), "write_file",
		map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, msgBus, nil)
	if isErr {
		t.Fatalf("expected success, got error: %s", out)
	}
	if out != llmPayload {
		t.Errorf("MCP response envelope must carry only ForLLM (%q), got %q", llmPayload, out)
	}
	if strings.Contains(out, userPayload) {
		t.Errorf("ForUser content must not appear in the MCP response envelope, got: %q", out)
	}
}

// TestSessionTokenStore_ReplyChatPerTurn is the unit test for the per-turn
// chat a call's output goes to: none without a turn, the turn's chat with
// one, ambiguous with several, the remaining turn's once one ends, and none
// for a service token whatever runs.
func TestSessionTokenStore_ReplyChatPerTurn(t *testing.T) {
	s := newSessionTokenStore()
	tok := s.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	svc := "SST" + strings.Repeat("ab", 32)
	s.RegisterService(svc, "alice", "/ws/alice/sessions")
	chat := func(token string) (tools.ReplyChat, bool) {
		t.Helper()
		rec, ok := s.Resolve(token)
		if !ok {
			t.Fatal("expected token to resolve")
		}
		return s.ReplyChat(rec)
	}

	if rc, ok := chat(tok); ok {
		t.Fatalf("no turn: ReplyChat = %+v, want none", rc)
	}
	endSlack := s.BeginTurn("agent:alice:main", "slack", "C123")
	if rc, ok := chat(tok); !ok || rc != (tools.ReplyChat{Channel: "slack", ChatID: "C123"}) {
		t.Fatalf("one turn: ReplyChat = %+v %v, want slack/C123", rc, ok)
	}
	endTelegram := s.BeginTurn("agent:alice:main", "telegram", "789")
	if rc, ok := chat(tok); !ok || !rc.Ambiguous {
		t.Fatalf("two turns: ReplyChat = %+v %v, want ambiguous", rc, ok)
	}
	if ch, id := (tools.ReplyChat{Channel: "slack", ChatID: "C1", Ambiguous: true}).Target(); ch != "" || id != "" {
		t.Fatalf("ambiguous Target = %s/%s, want none", ch, id)
	}
	if rc, ok := chat(svc); ok {
		t.Fatalf("service token: ReplyChat = %+v, want none", rc)
	}
	endSlack()
	if rc, ok := chat(tok); !ok || rc != (tools.ReplyChat{Channel: "telegram", ChatID: "789"}) {
		t.Fatalf("after the first ends: ReplyChat = %+v %v, want telegram/789", rc, ok)
	}
	endTelegram()
	if rc, ok := chat(tok); ok {
		t.Fatalf("all ended: ReplyChat = %+v, want none", rc)
	}
}
