// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/tools"
)

// scopeTool records the ask chain it ran with.
type scopeTool struct {
	chain []string
}

func (s *scopeTool) Name() string               { return "probe" }
func (s *scopeTool) Description() string        { return "records turn scope" }
func (s *scopeTool) Parameters() map[string]any { return map[string]any{} }
func (s *scopeTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	s.chain = tools.AskChain(ctx)
	return &tools.ToolResult{ForLLM: "ok", ForUser: "for the chat"}
}

func dispatchScope(t *testing.T, st *SessionTokenStore, tok string, msgBus *bus.MessageBus) *scopeTool {
	t.Helper()
	tool := &scopeTool{}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(tool)}
	if out, isErr := dispatchToolCall(context.Background(), "probe",
		map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, msgBus, nil); isErr {
		t.Fatalf("dispatch failed: %s", out)
	}
	return tool
}

// A CLI agent's tool calls carry the ask chain of the turn holding the
// token, as in-process calls do (so an agent waiting in the
// exchange cannot be asked over MCP either); a later turn replaces them, and
// a token rotation keeps them.
func TestDispatch_CarriesTurnScope(t *testing.T) {
	st := newSessionTokenStore()
	st.Issue("alice", "agent:alice:main", "/ws")

	st.SetTurnScope("agent:alice:main", []string{"bob", "alice"})
	tok := st.Issue("alice", "agent:alice:main", "/ws") // rotation keeps the scope
	got := dispatchScope(t, st, tok, nil)
	if !slices.Equal(got.chain, []string{"bob", "alice"}) {
		t.Fatalf("scope = %v, want [bob alice]", got.chain)
	}

	st.SetTurnScope("agent:alice:main", []string{"alice"})
	got = dispatchScope(t, st, tok, nil)
	if !slices.Equal(got.chain, []string{"alice"}) {
		t.Fatalf("scope after a later turn = %v, want [alice]", got.chain)
	}

	svc := "SST" + strings.Repeat("cd", 32)
	st.RegisterService(svc, "alice", "/ws")
	if got := dispatchScope(t, st, svc, nil); got.chain != nil {
		t.Fatalf("service token scope = %v, want none", got.chain)
	}
}

// While an asked turn holds the token its source is the ask: a tool's
// ForUser output is dropped rather than sent to the chat the session last
// heard from. Source reports what SetSource recorded.
func TestDispatch_AskSourceNeverReachesAChat(t *testing.T) {
	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws")
	st.SetSource("agent:alice:main", "telegram", "chat-1")
	if ch, chat := st.Source("agent:alice:main"); ch != "telegram" || chat != "chat-1" {
		t.Fatalf("Source = %s/%s, want telegram/chat-1", ch, chat)
	}
	st.SetSource("agent:alice:main", constants.AgentMessageChannel, "ask-1")

	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	dispatchScope(t, st, tok, msgBus)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if out, ok := msgBus.SubscribeOutbound(ctx); ok {
		t.Fatalf("an asked turn's ForUser was published: %+v", out)
	}
}
