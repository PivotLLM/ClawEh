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

// TestPublishMCPAsyncToLLM_ReinjectsCompletion verifies an async tool's
// completion (ForLLM) is re-injected as a "system" message routed to the
// session's recorded channel, so the primary LLM is notified without polling.
func TestPublishMCPAsyncToLLM_ReinjectsCompletion(t *testing.T) {
	msgBus := bus.NewMessageBus()
	rec := sessionRecord{agentID: "penny", sessionKey: "agent:penny:main", channel: "slack", chatID: "C9"}

	got := make(chan bus.InboundMessage, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if m, ok := msgBus.ConsumeInbound(ctx); ok {
			got <- m
		}
	}()
	// give the consumer a moment to subscribe
	time.Sleep(20 * time.Millisecond)

	publishMCPAsyncToLLM(msgBus, rec, "agent_spawn",
		&tools.ToolResult{ForLLM: `{"event":"completed","uuid":"abc"}`})

	select {
	case m := <-got:
		if m.Channel != "system" {
			t.Errorf("channel = %q, want system", m.Channel)
		}
		if m.ChatID != "slack:C9" {
			t.Errorf("chat_id = %q, want slack:C9", m.ChatID)
		}
		if m.Content == "" || m.SenderID != "async:agent_spawn" {
			t.Errorf("unexpected message: %+v", m)
		}
		// The spawner's session info must travel with the completion so it routes
		// back to the spawning agent's session (not the default agent).
		if m.SessionKey != "agent:penny:main" {
			t.Errorf("session_key = %q, want agent:penny:main", m.SessionKey)
		}
		if m.Metadata["preresolved_agent_id"] != "penny" {
			t.Errorf("preresolved_agent_id = %q, want penny", m.Metadata["preresolved_agent_id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a re-injected system inbound message")
	}
}

// TestPublishMCPAsyncToLLM_DropsWithoutChannel verifies no message is published
// when the session has no recorded channel (nothing to route to).
func TestPublishMCPAsyncToLLM_DropsWithoutChannel(t *testing.T) {
	msgBus := bus.NewMessageBus()
	rec := sessionRecord{agentID: "penny", sessionKey: "agent:penny:main"} // no channel/chatID

	published := make(chan struct{}, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if _, ok := msgBus.ConsumeInbound(ctx); ok {
			published <- struct{}{}
		}
	}()
	time.Sleep(20 * time.Millisecond)

	publishMCPAsyncToLLM(msgBus, rec, "agent_spawn", &tools.ToolResult{ForLLM: "x"})

	select {
	case <-published:
		t.Fatal("should not publish when there is no recorded channel")
	case <-time.After(400 * time.Millisecond):
		// expected: nothing published
	}
}
