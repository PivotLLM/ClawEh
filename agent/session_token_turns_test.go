package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
)

// recordingSTI is a SessionTokenIssuer that records the turns the loop
// begins on session tokens (their reply chats) and the depths it sets, so a
// test can assert what a CLI provider's MCP calls would see.
type recordingSTI struct {
	mu       sync.Mutex
	begun    []sourceCall
	depths   []depthCall
	turns    map[int]sourceCall
	nextTurn int
}

type depthCall struct {
	sessionKey string
	depth      int
}

type sourceCall struct {
	sessionKey string
	channel    string
	chatID     string
}

func (r *recordingSTI) Issue(_ /*agentID*/, _ /*sessionKey*/, _ /*archiveDir*/ string) string {
	// Returning empty is fine for these tests — context_manager.go only
	// stores the token when non-empty.
	return ""
}
func (r *recordingSTI) Revoke(_ string)      {}
func (r *recordingSTI) RevokeAgent(_ string) {}

func (r *recordingSTI) SetDepth(sessionKey string, depth int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.depths = append(r.depths, depthCall{sessionKey: sessionKey, depth: depth})
}

func (r *recordingSTI) SetTurnScope(string, []string) {}

// BeginTurn records the turn's reply chat (calls) and keeps it while it runs
// (activeTurns).
func (r *recordingSTI) BeginTurn(sessionKey, channel, chatID string) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turns == nil {
		r.turns = map[int]sourceCall{}
	}
	r.nextTurn++
	id := r.nextTurn
	c := sourceCall{sessionKey: sessionKey, channel: channel, chatID: chatID}
	r.turns[id] = c
	r.begun = append(r.begun, c)
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.turns, id)
	}
}

// activeTurns is the reply chats of the turns running now.
func (r *recordingSTI) activeTurns() []sourceCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sourceCall, 0, len(r.turns))
	for _, c := range r.turns {
		out = append(out, c)
	}
	return out
}

func (r *recordingSTI) depthCalls() []depthCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]depthCall(nil), r.depths...)
}

// calls is the reply chats of every turn begun, in order.
func (r *recordingSTI) calls() []sourceCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sourceCall(nil), r.begun...)
}

// TestRunAgentLoop_EachTurnRecordsItsOwnChat: two turns of one session, on
// different chats, each record their own chat on the token for as long as
// they run, and nothing is left once they end: a CLI provider's MCP output
// follows the turn it belongs to, never the chat the session last heard from.
func TestRunAgentLoop_EachTurnRecordsItsOwnChat(t *testing.T) {
	al := newTestAgentLoop(t).al
	agentInstance := al.registry.Default()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	rec := &recordingSTI{}
	al.SetSessionTokenIssuer(rec)
	var during [][]sourceCall
	agentInstance.Provider = &probeProvider{onChat: func() { during = append(during, rec.activeTurns()) }}

	opts := processOptions{SessionKey: "agent:main:main", Channel: "slack", ChatID: "C123", UserMessage: "hello from slack"}
	if _, err := al.runAgentLoop(context.Background(), agentInstance, opts); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	opts.Channel, opts.ChatID, opts.UserMessage = "telegram", "789", "hello from telegram"
	if _, err := al.runAgentLoop(context.Background(), agentInstance, opts); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	want := []sourceCall{
		{"agent:main:main", "slack", "C123"},
		{"agent:main:main", "telegram", "789"},
	}
	if len(during) != 2 {
		t.Fatalf("model calls = %d, want 2", len(during))
	}
	for i, w := range want {
		if len(during[i]) != 1 || during[i][0] != w {
			t.Fatalf("turn %d: turns on the token = %+v, want only %+v", i+1, during[i], w)
		}
	}
	if left := rec.activeTurns(); len(left) != 0 {
		t.Fatalf("turns after = %+v, want none", left)
	}
}

// turnsDuringModelCall runs run on a fresh loop whose default agent records,
// at its model call, the turns running on session tokens, after an earlier
// turn answered telegram chat 42 (the chat the session last heard from).
func turnsDuringModelCall(t *testing.T, run func(al *AgentLoop) error) []sourceCall {
	t.Helper()
	al := newTestAgentLoop(t).al
	agentInstance := al.registry.Default()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	rec := &recordingSTI{}
	al.SetSessionTokenIssuer(rec)
	var during []sourceCall
	agentInstance.Provider = &probeProvider{onChat: func() { during = rec.activeTurns() }}
	if _, err := al.runAgentLoop(context.Background(), agentInstance, processOptions{
		SessionKey: "agent:main:main", Channel: "telegram", ChatID: "42", UserMessage: "earlier",
	}); err != nil {
		t.Fatalf("earlier turn: %v", err)
	}
	during = nil
	if err := run(al); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if left := rec.activeTurns(); len(left) != 0 {
		t.Fatalf("turns after = %+v, want none", left)
	}
	return during
}

// TestTurnChat_PerTurnKind: the chat a CLI provider's MCP output goes to is
// the chat of the turn it runs in: a claw agent (cli) turn has only its
// internal channel (so msg_send and msg_send_file refuse), never the chat the
// session last heard from; a tool result's (system) turn answers the chat in
// its origin; a scheduled job's turn answers the chat it was sent to.
func TestTurnChat_PerTurnKind(t *testing.T) {
	for _, tc := range []struct {
		name          string
		run           func(al *AgentLoop) error
		channel, chat string
	}{
		{"claw agent (cli)", func(al *AgentLoop) error {
			_, err := al.ProcessDirect(context.Background(), "hi", "agent:main:main")
			return err
		}, "cli", "direct"},
		{"tool result (system)", func(al *AgentLoop) error {
			_, err := al.processSystemMessage(context.Background(), bus.InboundMessage{
				Channel: "system", SenderID: "async:agent_spawn", ChatID: "C9", Content: "Result:\nfine",
				Metadata: map[string]string{bus.MetaOriginChannel: "slack"},
			}, nil)
			return err
		}, "slack", "C9"},
		{"scheduled job", func(al *AgentLoop) error {
			_, err := al.ProcessDirectWithChannel(context.Background(), "reminder", "", "slack", "C7", "channel")
			return err
		}, "slack", "C7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			during := turnsDuringModelCall(t, tc.run)
			if len(during) != 1 || during[0].channel != tc.channel || during[0].chatID != tc.chat {
				t.Fatalf("turns on the token during the call = %+v, want only %s/%s", during, tc.channel, tc.chat)
			}
		})
	}
}
