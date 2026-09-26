package agent

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ctxengine/memory"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/state"
)

// recoveryTestStore is a minimal in-memory SessionStore for recovery tests.
type recoveryTestStore struct {
	history        []providers.Message
	clearCalled    bool
	pendingCleared string
}

func (s *recoveryTestStore) GetHistory(_ string) []providers.Message {
	cp := make([]providers.Message, len(s.history))
	copy(cp, s.history)
	return cp
}

func (s *recoveryTestStore) SetHistory(_ string, h []providers.Message) error {
	cp := make([]providers.Message, len(h))
	copy(cp, h)
	s.history = cp
	return nil
}
func (s *recoveryTestStore) AddMessage(_, _, _ string) error { return nil }
func (s *recoveryTestStore) AddFullMessage(_ string, _ providers.Message) (int64, error) {
	return 0, nil
}
func (s *recoveryTestStore) GetSummary(_ string) string            { return "" }
func (s *recoveryTestStore) SetSummary(_, _ string) error          { return nil }
func (s *recoveryTestStore) TruncateHistory(_ string, _ int) error { return nil }
func (s *recoveryTestStore) SetPendingTurn(_ string) error         { return nil }
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

// recoveryTestChannel is a registered-but-inert channel so the manager reports it configured.
type recoveryTestChannel struct{ *channels.BaseChannel }

func (recoveryTestChannel) Start(context.Context) error                     { return nil }
func (recoveryTestChannel) Stop(context.Context) error                      { return nil }
func (recoveryTestChannel) Send(context.Context, bus.OutboundMessage) error { return nil }

// newRecoveryTestLoop returns a loop whose channel manager knows only "webui",
// with a store holding one conversation.
func newRecoveryTestLoop(t *testing.T) (*testLoop, *recoveryTestStore) {
	t.Helper()
	tl := newTestAgentLoop(t)
	cm, err := channels.NewManager(&config.Config{}, tl.msgBus, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	cm.RegisterChannel("webui", recoveryTestChannel{channels.NewBaseChannel("webui", nil, tl.msgBus, nil)})
	tl.al.SetChannelManager(cm)
	store := &recoveryTestStore{
		history: []providers.Message{
			{Role: "user", Content: "what is 2+2"},
			{Role: "assistant", Content: "4"},
			{Role: "user", Content: "and 3+3?"},
		},
	}
	return tl, store
}

func mustGetAgent(t *testing.T, al *AgentLoop) *AgentInstance {
	t.Helper()
	agent, ok := al.GetRegistry().GetAgent("main")
	if !ok {
		t.Fatal("agent main not registered")
	}
	return agent
}

func recordSource(t *testing.T, al *AgentLoop, channel, chatID string) *state.Manager {
	t.Helper()
	sm := al.agentStates["main"]
	if err := sm.SetPendingTurn(testSessionKey, state.PendingTurn{Channel: channel, ChatID: chatID}); err != nil {
		t.Fatalf("SetPendingTurn: %v", err)
	}
	return sm
}

func consumeInbound(t *testing.T, mb *bus.MessageBus) (bus.InboundMessage, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	return mb.ConsumeInbound(ctx)
}

func consumeOutbound(t *testing.T, mb *bus.MessageBus) (bus.OutboundMessage, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	return mb.SubscribeOutbound(ctx)
}

func TestRecoverSession_ReplaysOnOriginalChannel(t *testing.T) {
	tl, store := newRecoveryTestLoop(t)
	sm := recordSource(t, tl.al, "webui", "chat-7")

	tl.al.recoverSession(context.Background(), "main", testSessionKey, store)

	notice, ok := consumeOutbound(t, tl.msgBus)
	if !ok {
		t.Fatal("expected a restart notice on the outbound bus")
	}
	if notice.Channel != "webui" || notice.ChatID != "chat-7" || notice.Content != recoveryReplayNotice {
		t.Errorf("notice = %+v, want replay notice on webui/chat-7", notice)
	}

	recovered, ok := consumeInbound(t, tl.msgBus)
	if !ok {
		t.Fatal("expected recovery message to be published on bus, none found")
	}
	if recovered.Channel != "webui" || recovered.ChatID != "chat-7" {
		t.Errorf("Channel/ChatID = %q/%q, want webui/chat-7", recovered.Channel, recovered.ChatID)
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
		t.Error("ClearPendingTurn should NOT be called when the turn is replayed")
	}
	pt, ok := sm.GetPendingTurn(testSessionKey)
	if !ok || pt.Attempts != 1 {
		t.Errorf("recorded attempts = %+v (ok=%v), want Attempts 1", pt, ok)
	}

	// The replayed turn re-records its source; the attempt count must survive.
	tl.al.recordPendingTurnSource(mustGetAgent(t, tl.al), processOptions{
		SessionKey: testSessionKey, Channel: "webui", ChatID: "chat-7", IsRetry: true,
	})
	if pt, _ := sm.GetPendingTurn(testSessionKey); pt.Attempts != 1 {
		t.Errorf("attempts after re-record = %d, want 1", pt.Attempts)
	}
}

func TestRecoverSession_CapGivesUpWithNotice(t *testing.T) {
	tl, store := newRecoveryTestLoop(t)
	sm := recordSource(t, tl.al, "webui", "chat-7")
	ctx := context.Background()

	// Two restarts replay; drain their notices and replays.
	for i := 1; i <= recoveryMaxAttempts; i++ {
		tl.al.recoverSession(ctx, "main", testSessionKey, store)
		if _, ok := consumeOutbound(t, tl.msgBus); !ok {
			t.Fatalf("restart %d: expected replay notice", i)
		}
		if _, ok := consumeInbound(t, tl.msgBus); !ok {
			t.Fatalf("restart %d: expected replay", i)
		}
		if pt, _ := sm.GetPendingTurn(testSessionKey); pt.Attempts != i {
			t.Fatalf("restart %d: attempts = %d", i, pt.Attempts)
		}
	}

	// The third restart gives up.
	tl.al.recoverSession(ctx, "main", testSessionKey, store)
	notice, ok := consumeOutbound(t, tl.msgBus)
	if !ok {
		t.Fatal("expected give-up notice")
	}
	if notice.Channel != "webui" || notice.ChatID != "chat-7" || notice.Content != recoveryGiveUpNotice {
		t.Errorf("notice = %+v, want give-up notice on webui/chat-7", notice)
	}
	if _, ok := consumeInbound(t, tl.msgBus); ok {
		t.Error("no replay expected once the cap is reached")
	}
	if !store.clearCalled || store.pendingCleared != testSessionKey {
		t.Errorf("pending flag not cleared (called=%v key=%q)", store.clearCalled, store.pendingCleared)
	}
	if _, ok := sm.GetPendingTurn(testSessionKey); ok {
		t.Error("source record should be cleared once the cap is reached")
	}
}

func TestRecoverSession_MissingChannelClears(t *testing.T) {
	tl, store := newRecoveryTestLoop(t)
	sm := recordSource(t, tl.al, "telegram", "42")

	tl.al.recoverSession(context.Background(), "main", testSessionKey, store)

	if !store.clearCalled {
		t.Error("ClearPendingTurn should be called when the channel is gone")
	}
	if _, ok := sm.GetPendingTurn(testSessionKey); ok {
		t.Error("source record should be cleared when the channel is gone")
	}
	if _, ok := consumeInbound(t, tl.msgBus); ok {
		t.Error("no replay expected when the channel is gone")
	}
	if _, ok := consumeOutbound(t, tl.msgBus); ok {
		t.Error("no notice can be sent when the channel is gone")
	}
}

func TestRecoverSession_NoSourceClears(t *testing.T) {
	tl, store := newRecoveryTestLoop(t)

	tl.al.recoverSession(context.Background(), "main", testSessionKey, store)

	if !store.clearCalled || store.pendingCleared != testSessionKey {
		t.Errorf("ClearPendingTurn not called for %q (called=%v key=%q)", testSessionKey, store.clearCalled, store.pendingCleared)
	}
	if _, ok := consumeInbound(t, tl.msgBus); ok {
		t.Error("no replay expected without a recorded source")
	}
}

func TestRecoverSession_NoUserMessageClears(t *testing.T) {
	tl, store := newRecoveryTestLoop(t)
	store.history = []providers.Message{{Role: "assistant", Content: "hello"}}
	recordSource(t, tl.al, "webui", "chat-7")

	tl.al.recoverSession(context.Background(), "main", testSessionKey, store)

	if !store.clearCalled {
		t.Error("ClearPendingTurn should be called when no user message is found")
	}
	if _, ok := consumeInbound(t, tl.msgBus); ok {
		t.Error("no replay expected without a user message")
	}
}

func TestRecordPendingTurnSource_SkipsInternalChannels(t *testing.T) {
	tl := newTestAgentLoop(t)
	agent := mustGetAgent(t, tl.al)
	for _, ch := range []string{"cli", "system", "subagent", "recovery", ""} {
		tl.al.recordPendingTurnSource(agent, processOptions{SessionKey: testSessionKey, Channel: ch, ChatID: "x"})
		if _, ok := tl.al.agentStates["main"].GetPendingTurn(testSessionKey); ok {
			t.Errorf("channel %q must not be recorded", ch)
		}
	}
	tl.al.recordPendingTurnSource(agent, processOptions{SessionKey: testSessionKey, Channel: "webui", ChatID: "x"})
	if pt, ok := tl.al.agentStates["main"].GetPendingTurn(testSessionKey); !ok || pt.Channel != "webui" || pt.ChatID != "x" {
		t.Errorf("recorded = %+v (ok=%v), want webui/x", pt, ok)
	}
	tl.al.clearPendingTurnSource("main", testSessionKey)
	if _, ok := tl.al.agentStates["main"].GetPendingTurn(testSessionKey); ok {
		t.Error("record should be cleared")
	}
}
