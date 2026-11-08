// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/tools"
	toolssession "github.com/PivotLLM/ClawEh/tools/session"
)

// session_clear is refused for a CLI agent while its token's source is an
// ask (its asked turn), and works from a chat.
func TestDispatch_SessionClearRefusedInAskedTurn(t *testing.T) {
	cleared := 0
	deps := tools.ToolDeps{
		AgentID: "alice",
		ClearFn: func(context.Context, string, string) error { cleared++; return nil },
	}
	reg := tools.NewToolRegistry()
	for _, tl := range tools.NamespacedProvider("session", toolssession.GlobalProvider).Build(deps) {
		reg.Register(tl)
	}
	regs := map[string]*tools.ToolRegistry{"alice": reg}

	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws")
	call := func() (string, bool) {
		return dispatchToolCall(context.Background(), "session_clear",
			map[string]any{"session_token": tok}, st, resolverFor(regs), nil, nil, nil, nil)
	}

	endAsk := st.BeginTurn("agent:alice:main", constants.AgentMessageChannel, "ask-1")
	if out, isErr := call(); !isErr || !strings.Contains(out, "not available while answering another agent's message") || cleared != 0 {
		t.Fatalf("asked turn: %q isErr=%v cleared=%d, want the refusal", out, isErr, cleared)
	}
	// A chat turn beside the ask: the call can't be tied to one of them.
	endChat := st.BeginTurn("agent:alice:main", "telegram", "chat-1")
	if out, isErr := call(); !isErr || !strings.Contains(out, "answering more than one conversation") || cleared != 0 {
		t.Fatalf("ask and chat turn: %q isErr=%v cleared=%d, want the refusal", out, isErr, cleared)
	}
	endAsk()
	if out, isErr := call(); isErr || cleared != 1 {
		t.Fatalf("chat turn: %q isErr=%v cleared=%d, want the clear queued", out, isErr, cleared)
	}
	endChat()
}
