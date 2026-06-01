package agent

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// recoveryTestStore is a minimal in-memory SessionStore for recovery tests.
type recoveryTestStore struct {
	history         []providers.Message
	clearCalled     bool
	pendingCleared  string
}

func (s *recoveryTestStore) GetHistory(_ string) []providers.Message {
	cp := make([]providers.Message, len(s.history))
	copy(cp, s.history)
	return cp
}
func (s *recoveryTestStore) SetHistory(_ string, h []providers.Message) {
	cp := make([]providers.Message, len(h))
	copy(cp, h)
	s.history = cp
}
func (s *recoveryTestStore) AddMessage(_, _, _ string)                    {}
func (s *recoveryTestStore) AddFullMessage(_ string, _ providers.Message) int64 { return 0 }
func (s *recoveryTestStore) GetSummary(_ string) string                   { return "" }
func (s *recoveryTestStore) SetSummary(_, _ string)                       {}
func (s *recoveryTestStore) TruncateHistory(_ string, _ int)              {}
func (s *recoveryTestStore) SetPendingTurn(_ string) error                { return nil }
func (s *recoveryTestStore) ClearPendingTurn(key string) error {
	s.clearCalled = true
	s.pendingCleared = key
	return nil
}
func (s *recoveryTestStore) GetArchiveBounds(_ string) (int64, int64) { return 0, 0 }
func (s *recoveryTestStore) GetHistoryWithSeqs(_ string) []memory.StoredMessage {
	stored := make([]memory.StoredMessage, len(s.history))
	for i, msg := range s.history {
		stored[i] = memory.StoredMessage{Seq: int64(i + 1), Message: msg}
	}
	return stored
}
func (s *recoveryTestStore) ListPendingSessions() ([]string, error) {
	return nil, nil
}
func (s *recoveryTestStore) Save(_ string) error { return nil }
func (s *recoveryTestStore) Close() error        { return nil }

const testSessionKey = "agent:main:webui:direct:webui:test-session"

func TestRecoverSession_WithUserMessage(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	store := &recoveryTestStore{
		history: []providers.Message{
			{Role: "user", Content: "what is 2+2"},
			{Role: "assistant", Content: "4"},
			{Role: "user", Content: "and 3+3?"},
		},
	}

	ctx := context.Background()
	al.recoverSession(ctx, "main", testSessionKey, store)

	// Consume the published message with a timeout.
	msgBus.PublishInbound(ctx, bus.InboundMessage{Content: "sentinel"}) //nolint:errcheck
	var recovered bus.InboundMessage
	found := false
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		msg, ok := msgBus.ConsumeInbound(ctx)
		if !ok {
			break
		}
		if msg.Channel == "recovery" {
			recovered = msg
			found = true
			break
		}
	}

	if !found {
		t.Fatal("expected recovery message to be published on bus, none found")
	}
	if recovered.Channel != "recovery" {
		t.Errorf("Channel = %q, want %q", recovered.Channel, "recovery")
	}
	if recovered.SenderID != "recovery" {
		t.Errorf("SenderID = %q, want %q", recovered.SenderID, "recovery")
	}
	if recovered.Content != "and 3+3?" {
		t.Errorf("Content = %q, want %q", recovered.Content, "and 3+3?")
	}
	if recovered.SessionKey != testSessionKey {
		t.Errorf("SessionKey = %q, want %q", recovered.SessionKey, testSessionKey)
	}
	if !recovered.IsRetry {
		t.Error("IsRetry should be true")
	}
	if recovered.Metadata["preresolved_agent_id"] != "main" {
		t.Errorf("preresolved_agent_id = %q, want %q", recovered.Metadata["preresolved_agent_id"], "main")
	}

	if store.clearCalled {
		t.Error("ClearPendingTurn should NOT be called when user message is found")
	}
}

func TestRecoverSession_NoUserMessage(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	store := &recoveryTestStore{
		history: []providers.Message{
			{Role: "assistant", Content: "hello"},
		},
	}

	ctx := context.Background()
	al.recoverSession(ctx, "main", testSessionKey, store)

	if !store.clearCalled {
		t.Error("ClearPendingTurn should be called when no user message is found")
	}
	if store.pendingCleared != testSessionKey {
		t.Errorf("ClearPendingTurn called with %q, want %q", store.pendingCleared, testSessionKey)
	}
}

func TestRecoverSession_EmptyHistory(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	store := &recoveryTestStore{
		history: []providers.Message{},
	}

	ctx := context.Background()
	al.recoverSession(ctx, "main", testSessionKey, store)

	if !store.clearCalled {
		t.Error("ClearPendingTurn should be called for empty history")
	}
}
