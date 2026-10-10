package agent

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/PivotLLM/ctxengine/memory"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/state"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
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

const testSessionKey = "agent:main:main"

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
	agent, ok := al.GetRegistry().Get("main")
	if !ok {
		t.Fatal("agent main not registered")
	}
	return agent
}

func recordSource(t *testing.T, al *AgentLoop, channel, chatID string) *state.Manager {
	t.Helper()
	sm := testStateManager(t, al, "main")
	if err := sm.SetPendingTurn(testSessionKey, state.PendingTurn{Channel: channel, ChatID: chatID}); err != nil {
		t.Fatalf("SetPendingTurn: %v", err)
	}
	return sm
}

// consumeInbound returns the next queued inbound message. recoverSession
// publishes before it returns, so the short bound only matters when nothing
// was queued; noInbound checks that nothing was.
func consumeInbound(t *testing.T, mb *bus.MessageBus) (bus.InboundMessage, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	return mb.ConsumeInbound(ctx)
}

// consumeOutbound is consumeInbound for the outbound queue.
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
	tl.al.recordPendingTurnSource(context.Background(), mustGetAgent(t, tl.al), processOptions{
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
	noInbound(t, tl.msgBus) // no replay expected once the cap is reached
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
	noInbound(t, tl.msgBus)  // no replay expected when the channel is gone
	noOutbound(t, tl.msgBus) // no notice can be sent when the channel is gone
}

func TestRecoverSession_NoSourceClears(t *testing.T) {
	tl, store := newRecoveryTestLoop(t)

	tl.al.recoverSession(context.Background(), "main", testSessionKey, store)

	if !store.clearCalled || store.pendingCleared != testSessionKey {
		t.Errorf("ClearPendingTurn not called for %q (called=%v key=%q)", testSessionKey, store.clearCalled, store.pendingCleared)
	}
	noInbound(t, tl.msgBus) // no replay expected without a recorded source
}

// A pending turn in a session no turn runs in any more (a per-sender key from
// an earlier release) is cleared, not replayed into the main session.
func TestRecoverSession_UnusedSessionClears(t *testing.T) {
	tl, store := newRecoveryTestLoop(t)
	const oldKey = "agent:main:webui:direct:webui:test-session"
	sm := testStateManager(t, tl.al, "main")
	if err := sm.SetPendingTurn(oldKey, state.PendingTurn{Channel: "webui", ChatID: "chat-7"}); err != nil {
		t.Fatalf("SetPendingTurn: %v", err)
	}

	tl.al.recoverSession(context.Background(), "main", oldKey, store)

	if !store.clearCalled || store.pendingCleared != oldKey {
		t.Errorf("ClearPendingTurn not called for %q (called=%v key=%q)", oldKey, store.clearCalled, store.pendingCleared)
	}
	if _, ok := sm.GetPendingTurn(oldKey); ok {
		t.Error("recorded source not cleared")
	}
	noInbound(t, tl.msgBus)  // no replay expected for an unused session
	noOutbound(t, tl.msgBus) // no notice expected for an unused session
}

func TestRecoverSession_NoUserMessageClears(t *testing.T) {
	tl, store := newRecoveryTestLoop(t)
	store.history = []providers.Message{{Role: "assistant", Content: "hello"}}
	recordSource(t, tl.al, "webui", "chat-7")

	tl.al.recoverSession(context.Background(), "main", testSessionKey, store)

	if !store.clearCalled {
		t.Error("ClearPendingTurn should be called when no user message is found")
	}
	noInbound(t, tl.msgBus) // no replay expected without a user message
}

func TestRecordPendingTurnSource_SkipsInternalChannels(t *testing.T) {
	tl := newTestAgentLoop(t)
	agent := mustGetAgent(t, tl.al)
	for _, ch := range []string{"cli", "system", "subagent", "recovery", ""} {
		tl.al.recordPendingTurnSource(context.Background(), agent, processOptions{SessionKey: testSessionKey, Channel: ch, ChatID: "x"})
		if _, ok := testStateManager(t, tl.al, "main").GetPendingTurn(testSessionKey); ok {
			t.Errorf("channel %q must not be recorded", ch)
		}
	}
	tl.al.recordPendingTurnSource(context.Background(), agent, processOptions{SessionKey: testSessionKey, Channel: "webui", ChatID: "x"})
	if pt, ok := testStateManager(t, tl.al, "main").GetPendingTurn(testSessionKey); !ok || pt.Channel != "webui" || pt.ChatID != "x" {
		t.Errorf("recorded = %+v (ok=%v), want webui/x", pt, ok)
	}
	tl.al.clearPendingTurnSource("main", testSessionKey)
	if _, ok := testStateManager(t, tl.al, "main").GetPendingTurn(testSessionKey); ok {
		t.Error("record should be cleared")
	}
}

// TestRecordPendingTurnSource_KeepsReplyContract: the pending turn records the
// inbound message id, reply_required and the turn's spawn depth.
func TestRecordPendingTurnSource_KeepsReplyContract(t *testing.T) {
	tl := newTestAgentLoop(t)
	ctx := toolsagents.WithSpawnDepth(context.Background(), 2)
	tl.al.recordPendingTurnSource(ctx, mustGetAgent(t, tl.al), processOptions{
		SessionKey: testSessionKey, Channel: "forum", ChatID: "f/w/1", MessageID: "m1", ReplyRequired: true,
	})
	pt, ok := testStateManager(t, tl.al, "main").GetPendingTurn(testSessionKey)
	if !ok || pt.MessageID != "m1" || !pt.ReplyRequired || pt.SpawnDepth != 2 {
		t.Fatalf("recorded = %+v (ok=%v), want m1, reply required, depth 2", pt, ok)
	}
}

// TestRecoverSession_ReplayKeepsReplyContract: a replayed turn carries the
// original message id, reply_required and spawn_depth again; without them
// the replay carries neither flag.
func TestRecoverSession_ReplayKeepsReplyContract(t *testing.T) {
	for _, flagged := range []bool{false, true} {
		tl, store := newRecoveryTestLoop(t)
		pt := state.PendingTurn{Channel: "webui", ChatID: "chat-7", MessageID: "m1"}
		if flagged {
			pt.ReplyRequired, pt.SpawnDepth = true, 2
		}
		if err := testStateManager(t, tl.al, "main").SetPendingTurn(testSessionKey, pt); err != nil {
			t.Fatal(err)
		}
		tl.al.recoverSession(context.Background(), "main", testSessionKey, store)
		consumeOutbound(t, tl.msgBus) // the replay notice
		msg, ok := consumeInbound(t, tl.msgBus)
		if !ok {
			t.Fatal("no replay")
		}
		if msg.MessageID != "m1" || msg.ReplyRequired() != flagged {
			t.Fatalf("flagged=%v: replay = %+v, want message m1 and reply_required %v", flagged, msg, flagged)
		}
		wantDepth := ""
		if flagged {
			wantDepth = "2"
		}
		if got := msg.Metadata[bus.MetaSpawnDepth]; got != wantDepth {
			t.Fatalf("flagged=%v: spawn_depth = %q, want %q", flagged, got, wantDepth)
		}
	}
}

// TestRecoverSession_GiveUpOnRequiredReplies: when recovery gives up on a turn
// whose sender required a reply, that reply is sent: an error outcome
// addressed to the original message. Without the flag the plain notice goes
// out, with no outcome.
func TestRecoverSession_GiveUpOnRequiredReplies(t *testing.T) {
	for _, flagged := range []bool{false, true} {
		tl, store := newRecoveryTestLoop(t)
		pt := state.PendingTurn{Channel: "webui", ChatID: "chat-7", MessageID: "m1", ReplyRequired: flagged, Attempts: recoveryMaxAttempts}
		if err := testStateManager(t, tl.al, "main").SetPendingTurn(testSessionKey, pt); err != nil {
			t.Fatal(err)
		}
		tl.al.recoverSession(context.Background(), "main", testSessionKey, store)
		out, ok := consumeOutbound(t, tl.msgBus)
		if !ok || out.Content != recoveryGiveUpNotice {
			t.Fatalf("flagged=%v: give-up = %+v (ok=%v)", flagged, out, ok)
		}
		if flagged && (out.Outcome != bus.OutcomeError || out.OriginalMessageID != "m1") {
			t.Fatalf("give-up reply = %+v, want an error outcome to m1", out)
		}
		if !flagged && (out.Outcome != "" || out.OriginalMessageID != "") {
			t.Fatalf("give-up notice = %+v, want no outcome", out)
		}
		noOutbound(t, tl.msgBus) // no second outbound
	}

	// No user message to replay: a flagged turn still gets its error reply.
	tl, store := newRecoveryTestLoop(t)
	store.history = []providers.Message{{Role: "assistant", Content: "hello"}}
	if err := testStateManager(t, tl.al, "main").SetPendingTurn(testSessionKey,
		state.PendingTurn{Channel: "webui", ChatID: "chat-7", MessageID: "m1", ReplyRequired: true}); err != nil {
		t.Fatal(err)
	}
	tl.al.recoverSession(context.Background(), "main", testSessionKey, store)
	if out, ok := consumeOutbound(t, tl.msgBus); !ok || out.Outcome != bus.OutcomeError || out.OriginalMessageID != "m1" {
		t.Fatalf("no-history give-up = %+v (ok=%v), want an error outcome to m1", out, ok)
	}
}

// testStateManager is the recovery state manager of config agent id.
func testStateManager(t *testing.T, al *AgentLoop, id string) *state.Manager {
	t.Helper()
	sm, ok := al.stateManager(id)
	if !ok {
		t.Fatalf("no state manager for %s", id)
	}
	return sm
}

// An agent a reload adds records where its turns come from like any other, so
// a restart can replay its interrupted turn: the source is on disk for the
// next process.
func TestRecordPendingTurnSource_AgentAddedByReload(t *testing.T) {
	tl := newTestAgentLoop(t)
	next := *tl.cfg
	next.Agents.List = append(slices.Clone(tl.cfg.Agents.List), config.AgentConfig{ID: "bob", Name: "Bob"})
	if err := tl.al.ReloadProviderAndConfig(context.Background(), tl.provider, &next); err != nil {
		t.Fatalf("ReloadProviderAndConfig: %v", err)
	}
	bob, ok := tl.al.GetRegistry().Get("bob")
	if !ok {
		t.Fatal("the reload did not add bob")
	}
	const key = "agent:bob:main"
	tl.al.recordPendingTurnSource(context.Background(), bob,
		processOptions{SessionKey: key, Channel: "webui", ChatID: "x", ReplyRequired: true})

	// What the next process reads at startup.
	pt, ok := state.NewManager(bob.Workspace).GetPendingTurn(key)
	if !ok || pt.Channel != "webui" || pt.ChatID != "x" || !pt.ReplyRequired {
		t.Fatalf("recorded source = %+v (ok=%v), want webui/x, reply required", pt, ok)
	}
}
