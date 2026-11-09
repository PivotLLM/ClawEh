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

	out, isErr := dispatchToolCall(withToolOutput(context.Background(), toolOutputAlways), "write_file",
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

// toolOutputAlways turns tool output on (stream_tool_activity true).
func toolOutputAlways() bool { return true }

// A tool's own output reaches the chat only with tool output on, as in the
// agent loop: with the switch off, or none set, nothing is published (a CLI
// agent's shell_exec output must not flood the chat), and the call's ForLLM is
// unaffected. The switch is read on every call.
func TestDispatch_ForUserOnlyWithToolOutputOn(t *testing.T) {
	rf := &mockTool{
		name:   "write_file",
		params: map[string]any{},
		result: &tools.ToolResult{ForLLM: "wrote ok", ForUser: "command output"},
	}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}
	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	defer st.BeginTurn("agent:alice:main", "slack", "C123")()

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	collected := make(chan bus.OutboundMessage, 4)
	go func() {
		for {
			msg, ok := msgBus.SubscribeOutbound(t.Context())
			if !ok {
				return
			}
			collected <- msg
		}
	}()

	on := false
	cases := []struct {
		name string
		ctx  context.Context
		on   bool
		want bool
	}{
		{"no switch", context.Background(), false, false},
		{"switch off", withToolOutput(context.Background(), func() bool { return on }), false, false},
		{"switch on", withToolOutput(context.Background(), func() bool { return on }), true, true},
		{"switch off again", withToolOutput(context.Background(), func() bool { return on }), false, false},
	}
	for _, tc := range cases {
		on = tc.on
		out, isErr := dispatchToolCall(tc.ctx, "write_file",
			map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, msgBus, nil)
		if isErr || out != "wrote ok" {
			t.Fatalf("%s: got %q (error %v), want the ForLLM", tc.name, out, isErr)
		}
		select {
		case got := <-collected:
			if !tc.want {
				t.Fatalf("%s: published %+v, want nothing", tc.name, got)
			}
			if got.Channel != "slack" || got.ChatID != "C123" || got.Content != "command output" {
				t.Fatalf("%s: published %+v, want the output to slack/C123", tc.name, got)
			}
		case <-time.After(150 * time.Millisecond):
			if tc.want {
				t.Fatalf("%s: nothing published, want the output", tc.name)
			}
		}
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

	out, isErr := dispatchToolCall(withToolOutput(context.Background(), toolOutputAlways), "write_file",
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

	out, isErr := dispatchToolCall(withToolOutput(context.Background(), toolOutputAlways), "write_file",
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

	out, isErr := dispatchToolCall(withToolOutput(context.Background(), toolOutputAlways), "write_file",
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

// WithToolOutput reaches the server's own tools/call handler: off (or not
// given) publishes nothing, on publishes the tool's output to the turn's chat.
func TestServer_WithToolOutput(t *testing.T) {
	rf := &mockTool{
		name:   "write_file",
		params: map[string]any{},
		result: &tools.ToolResult{ForLLM: "wrote ok", ForUser: "command output"},
	}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}
	store := NewSessionTokenStore()
	tok := store.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	defer store.BeginTurn("agent:alice:main", "slack", "C123")()

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	collected := make(chan bus.OutboundMessage, 4)
	go func() {
		for {
			msg, ok := msgBus.SubscribeOutbound(t.Context())
			if !ok {
				return
			}
			collected <- msg
		}
	}()

	on := false
	for _, tc := range []struct {
		name string
		opts []Option
		on   bool
		want bool
	}{
		{"not given", nil, false, false},
		{"off", []Option{WithToolOutput(func() bool { return on })}, false, false},
		{"on", []Option{WithToolOutput(func() bool { return on })}, true, true},
	} {
		on = tc.on
		srv := rebuild(t, nil, store, regs, append([]Option{WithMessageBus(msgBus)}, tc.opts...)...)
		if out, isErr := callTool(t, srv, "write_file", tok); isErr || out != "wrote ok" {
			t.Fatalf("%s: got %q (error %v)", tc.name, out, isErr)
		}
		select {
		case got := <-collected:
			if !tc.want || got.Content != "command output" || got.Channel != "slack" || got.ChatID != "C123" {
				t.Fatalf("%s: published %+v", tc.name, got)
			}
		case <-time.After(150 * time.Millisecond):
			if tc.want {
				t.Fatalf("%s: nothing published, want the output", tc.name)
			}
		}
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	}
}
