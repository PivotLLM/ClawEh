// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/tools"
)

// A temporary clone's late async result goes to its source's main
// conversation, resolved when the clone's token was issued, so it still
// arrives after the clone is gone; any other agent's goes to its own.
func TestPublishMCPAsyncToLLM_CloneResultGoesHome(t *testing.T) {
	st := newSessionTokenStore()
	st.SetHomeResolver(func(id string) string {
		if id == "c1" {
			return "alice"
		}
		return ""
	})
	cloneTok := st.Issue("c1", "agent:c1:main", "/tmp/c1/sessions")
	bobTok := st.Issue("bob", "agent:bob:main", "/tmp/bob/sessions")
	st.SetSource("agent:c1:main", "telegram", "42")
	st.SetSource("agent:bob:main", "telegram", "43")

	// The resolver no longer knows the clone (it was deleted): the record
	// keeps what it resolved at issue time.
	st.SetHomeResolver(func(string) string { return "" })

	for _, tc := range []struct {
		tok, wantAgent, wantSession string
	}{
		{cloneTok, "alice", "agent:alice:main"},
		{bobTok, "bob", "agent:bob:main"},
	} {
		rec, ok := st.Resolve(tc.tok)
		if !ok {
			t.Fatal("token not found")
		}
		msgBus := bus.NewMessageBus()
		got := make(chan bus.InboundMessage, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if m, ok := msgBus.ConsumeInbound(ctx); ok {
				got <- m
			}
		}()
		time.Sleep(20 * time.Millisecond)
		publishMCPAsyncToLLM(context.Background(), msgBus, rec, "agent_spawn", &tools.ToolResult{ForLLM: "done"})
		select {
		case m := <-got:
			if m.Metadata["preresolved_agent_id"] != tc.wantAgent || m.SessionKey != tc.wantSession {
				t.Errorf("result addressed to %q / %q, want %q / %q",
					m.Metadata["preresolved_agent_id"], m.SessionKey, tc.wantAgent, tc.wantSession)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no re-injected message")
		}
	}
}
