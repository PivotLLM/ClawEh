// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// TestDispatch_ForUserPublishedToOriginatingChannel confirms that when a tool
// returns ForUser content (Silent=false) and the session has a recorded
// channel + chatID, the MCP dispatch path publishes ForUser to the message
// bus targeted at the originating user — mirroring the inbound-user-message
// publish path at pkg/agent/loop.go.
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
	st.SetSource("agent:alice:main", "slack", "C123")

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	collected := make(chan bus.OutboundMessage, 4)
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
	st.SetSource("agent:alice:main", "slack", "C123")

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	collected := make(chan bus.OutboundMessage, 4)
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

// TestDispatch_ForUserDroppedWhenNoActiveChannel confirms that when a session
// has no recorded channel/chatID (e.g. a CLI-only session or a Dawn-initiated
// MCP call before any human chatted) and a tool returns ForUser, the MCP
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
	// Deliberately no SetSource — session has no recorded channel/chatID.

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	collected := make(chan bus.OutboundMessage, 4)
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
		t.Fatalf("expected no publish when session has no recorded channel, got %+v", got)
	case <-time.After(150 * time.Millisecond):
		// expected
	}

	logs := buf.String()
	if !strings.Contains(logs, `"message":"mcp.foruser.dropped"`) {
		t.Errorf("expected mcp.foruser.dropped log line, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"reason":"no_active_channel"`) {
		t.Errorf("expected reason=no_active_channel, got:\n%s", logs)
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
	st.SetSource("agent:alice:main", "slack", "C123")

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	// Drain outbound so the publish does not stall the test.
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
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

// TestSessionTokenStore_SetSourceWritesChannelChatID is the unit test for the
// inbound-message handler's per-session-record source tracking: SetSource on a
// known session key updates channel/chatID atomically on the recorded
// sessionRecord.
func TestSessionTokenStore_SetSourceWritesChannelChatID(t *testing.T) {
	s := newSessionTokenStore()
	tok := s.Issue("alice", "agent:alice:main", "/ws/alice/sessions")

	s.SetSource("agent:alice:main", "slack", "C123")

	rec, ok := s.Resolve(tok)
	if !ok {
		t.Fatal("expected token to resolve")
	}
	if rec.channel != "slack" {
		t.Errorf("expected channel=slack, got %q", rec.channel)
	}
	if rec.chatID != "C123" {
		t.Errorf("expected chatID=C123, got %q", rec.chatID)
	}

	// A second SetSource overwrites — unified-mode sessions follow the user
	// across channels by overwriting on each inbound turn.
	s.SetSource("agent:alice:main", "telegram", "789")
	rec, ok = s.Resolve(tok)
	if !ok {
		t.Fatal("expected token to still resolve after SetSource")
	}
	if rec.channel != "telegram" {
		t.Errorf("expected channel=telegram after overwrite, got %q", rec.channel)
	}
	if rec.chatID != "789" {
		t.Errorf("expected chatID=789 after overwrite, got %q", rec.chatID)
	}
}

// TestSessionTokenStore_SetSourceUnknownSessionIsNoop confirms SetSource for a
// session key that has no issued token is a no-op (no panic, no record
// created). This matches the "session token issued lazily" lifecycle: the
// agent loop may call SetSource before getContextManager has run.
func TestSessionTokenStore_SetSourceUnknownSessionIsNoop(t *testing.T) {
	s := newSessionTokenStore()
	s.SetSource("agent:nobody:main", "slack", "C123")
	// No assertion beyond "does not panic" — the source has no record to
	// land on, and the next Resolve for any token would not return it.
	if _, ok := s.Resolve("SST" + strings.Repeat("a", 64)); ok {
		t.Error("SetSource must not create a record for an unknown session")
	}
}
