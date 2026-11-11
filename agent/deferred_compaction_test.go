// ClawEh
// License: MIT

package agent

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/PivotLLM/ctxengine"
	"github.com/PivotLLM/ctxengine/memory"
	"github.com/PivotLLM/ctxengine/session"
	"github.com/PivotLLM/spawnllm/openai_compat"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

// compactMarkerChannel is the outbound channel dueContextManager queues a
// marker on when CompactIfDue is called, so a test reads from the bus's
// first-in, first-out queue whether the reply was queued before it.
const compactMarkerChannel = "test-compact-if-due"

// dueContextManager is a context manager whose CompactIfDue records the call
// and then blocks until release is closed or its context ends. onAdd, when
// set, runs inside AddUserMessage and Assemble with the turn's context (a
// test calls the compaction-start hook from it, as the engine would).
type dueContextManager struct {
	capturingContextManager
	msgBus  *bus.MessageBus
	calls   chan context.Context
	release chan struct{}
	onAdd   func(ctx context.Context)
}

func newDueContextManager(msgBus *bus.MessageBus) *dueContextManager {
	return &dueContextManager{msgBus: msgBus, calls: make(chan context.Context, 8), release: make(chan struct{})}
}

func (c *dueContextManager) AddUserMessage(ctx context.Context, msg providers.Message) (int64, error) {
	if c.onAdd != nil {
		c.onAdd(ctx)
	}
	return c.capturingContextManager.AddUserMessage(ctx, msg)
}

func (c *dueContextManager) Assemble(ctx context.Context, req ctxengine.AssembleRequest) (ctxengine.Assembly, error) {
	if c.onAdd != nil {
		c.onAdd(ctx)
	}
	return c.capturingContextManager.Assemble(ctx, req)
}

func (c *dueContextManager) CompactIfDue(ctx context.Context) (bool, error) {
	if c.msgBus != nil {
		if err := c.msgBus.PublishOutbound(context.WithoutCancel(ctx), bus.OutboundMessage{Channel: compactMarkerChannel}); err != nil {
			return false, err
		}
	}
	c.calls <- ctx
	select {
	case <-c.release:
		return true, nil
	case <-ctx.Done():
		return true, ctx.Err()
	}
}

// injectContextManager puts cm in al's cache for agent's session, so the
// turn uses it instead of building a real one.
func injectContextManager(al *AgentLoop, agentID, sessionKey string, cm ctxengine.ContextManager) {
	entry := &cmEntry{cm: cm, sessionKey: sessionKey}
	entry.touch()
	al.contextManagers.Store(agentID+":"+sessionKey, entry)
}

// inboundSessionKey is the agent and session an inbound message is handled in.
func inboundSessionKey(t *testing.T, al *AgentLoop, msg bus.InboundMessage) (*AgentInstance, string) {
	t.Helper()
	route, agent, err := al.resolveMessageRoute(msg)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	return agent, resolveScopeKey(route, msg.SessionKey)
}

// nextCompactCall returns the context of the next CompactIfDue call.
func nextCompactCall(t *testing.T, cm *dueContextManager) context.Context {
	t.Helper()
	select {
	case ctx := <-cm.calls:
		return ctx
	case <-time.After(30 * time.Second):
		t.Fatal("CompactIfDue was never called")
		return nil
	}
}

// TestDeferredCompaction_AfterReply: CompactIfDue runs once the turn's reply
// is queued, also for a turn that ends with no reply and for a system turn
// that sends its reply itself, on a context that is not the turn's (no turn
// id, still live once the turn is over) but names the agent.
func TestDeferredCompaction_AfterReply(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	for _, tc := range []struct {
		name      string
		msg       bus.InboundMessage
		resp      providers.LLMResponse
		wantReply bool
	}{
		{name: "reply", msg: inbound("c1", "id1", "hi"), resp: providers.LLMResponse{Content: "hello"}, wantReply: true},
		{
			name: "no reply",
			msg: bus.InboundMessage{
				Channel: "test", ChatID: "c1", SenderID: "u1", Content: "hi",
				Metadata: map[string]string{"is_group": "true"},
			},
			resp: providers.LLMResponse{FinishReason: "stop", Normal: true},
		},
		{
			name: "system",
			msg: bus.InboundMessage{
				Channel: "system", ChatID: "c1", SenderID: "async:job", Content: "done", Internal: true,
				Metadata: map[string]string{bus.MetaOriginChannel: "test"},
			},
			resp: providers.LLMResponse{Content: "hello"}, wantReply: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgBus := bus.NewMessageBus()
			al := mustNewAgentLoop(t, newTestConfig(t), msgBus, &fixedProvider{resp: &tc.resp}, nil)
			var agent *AgentInstance
			var sessionKey string
			if tc.msg.Channel == "system" {
				agent, sessionKey = al.resolveSystemMessageTarget(tc.msg)
			} else {
				agent, sessionKey = inboundSessionKey(t, al, tc.msg)
			}
			cm := newDueContextManager(msgBus)
			defer close(cm.release)
			injectContextManager(al, agent.ID, sessionKey, cm)

			dispatch(al, tc.msg)

			out := nextOutbound(t, msgBus)
			if tc.wantReply {
				if out.Content != "hello" || out.ChatID != "c1" {
					t.Fatalf("first outbound = %+v, want the reply to c1", out)
				}
				out = nextOutbound(t, msgBus)
			}
			if out.Channel != compactMarkerChannel {
				t.Fatalf("outbound = %+v, want CompactIfDue's marker after the reply", out)
			}
			ctx := nextCompactCall(t, cm)
			if id := turnIDFrom(ctx); id != "" {
				t.Errorf("CompactIfDue ran on the turn's context (turn id %s)", id)
			}
			if ctx.Err() != nil {
				t.Errorf("CompactIfDue's context ended with the turn: %v", ctx.Err())
			}
			if got := providers.AgentIDFromContext(ctx); got != agent.ID {
				t.Errorf("CompactIfDue's context names agent %q, want %q", got, agent.ID)
			}
		})
	}
}

// TestDeferredCompaction_NotStacked: while a session's deferred compaction
// runs, a turn ending in that session starts no second one; once it is over,
// the next turn starts one again.
func TestDeferredCompaction_NotStacked(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al := newTestAgentLoop(t).al
	agent := al.registry.Default()
	cm := newDueContextManager(nil)
	injectContextManager(al, agent.ID, "s1", cm)

	if !al.startDeferredCompaction(agent, "s1") {
		t.Fatal("the first deferred compaction did not start")
	}
	nextCompactCall(t, cm)
	if al.startDeferredCompaction(agent, "s1") {
		t.Fatal("a second deferred compaction started while the first runs")
	}
	close(cm.release)

	// The first one clears its mark on the way out; the next start succeeds.
	deadline := time.Now().Add(30 * time.Second)
	for !al.startDeferredCompaction(agent, "s1") {
		if time.Now().After(deadline) {
			t.Fatal("no deferred compaction started after the first one ended")
		}
		runtime.Gosched()
	}
	nextCompactCall(t, cm)
}

// TestDeferredCompaction_SkippedAgents: a sub-agent (ephemeral) and a
// single-shot agent keep no conversation, so nothing is deferred for them.
func TestDeferredCompaction_SkippedAgents(t *testing.T) {
	al := newTestAgentLoop(t).al
	for _, agent := range []*AgentInstance{
		{ID: "a1", Spec: agentreg.Spec{ID: "a1", Origin: agentreg.OriginTemp, Ephemeral: true}},
		{ID: "a2", Spec: agentreg.Spec{ID: "a2", Origin: agentreg.OriginTemp, Fresh: true, Mode: agentreg.ModeSingleShot}},
	} {
		cm := newDueContextManager(nil)
		injectContextManager(al, agent.ID, "s1", cm)
		al.compactAfterReply(nil, agent, "s1")
		if _, running := al.deferredCompactions.Load(agent.ID + ":s1"); running {
			t.Errorf("agent %s: a deferred compaction started", agent.ID)
		}
	}
}

// TestDeferredCompaction_ShutdownDoesNotWait: a running deferred compaction
// is not a turn, so waiting for the turns at shutdown does not wait for it,
// and stopping the loop cancels it.
func TestDeferredCompaction_ShutdownDoesNotWait(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al := newTestAgentLoop(t).al
	runCtx, stopRun := context.WithCancelCause(context.Background())
	al.runMu.Lock()
	al.runCtx, al.stopRun = runCtx, stopRun
	al.runMu.Unlock()

	agent := al.registry.Default()
	cm := newDueContextManager(nil)
	injectContextManager(al, agent.ID, "s1", cm)
	if !al.startDeferredCompaction(agent, "s1") {
		t.Fatal("the deferred compaction did not start")
	}
	ctx := nextCompactCall(t, cm)

	al.Stop()
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := al.WaitTurns(waitCtx); err != nil {
		t.Fatalf("WaitTurns: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("stopping the loop did not cancel the deferred compaction")
	}
	if al.startDeferredCompaction(agent, "s2") {
		t.Error("a deferred compaction started after shutdown")
	}
}

// TestDeferredCompaction_WaitsForTheTurnsQueue: a turn run under runTurn's
// after-reply queue starts nothing itself; the deferred compaction starts
// only when the queue runs, after the reply.
func TestDeferredCompaction_WaitsForTheTurnsQueue(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	msgBus := bus.NewMessageBus()
	al := mustNewAgentLoop(t, newTestConfig(t), msgBus, &fixedProvider{resp: &providers.LLMResponse{Content: "hello"}}, nil)
	agent := al.registry.Default()
	cm := newDueContextManager(nil)
	defer close(cm.release)
	injectContextManager(al, agent.ID, "s1", cm)

	ctx, queue := withAfterReply(context.Background())
	if _, err := al.runAgentLoop(ctx, agent, processOptions{SessionKey: "s1", Channel: "test", ChatID: "c1", UserMessage: "hi"}); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if _, running := al.deferredCompactions.Load(agent.ID + ":s1"); running {
		t.Fatal("the deferred compaction started before the turn's queue ran")
	}
	queue.run()
	nextCompactCall(t, cm)
}

// ageProvider answers a chat with "hello" and a summarization request (asked
// for a JSON object) with a summary, reporting each summarization on summaries.
type ageProvider struct {
	summaries chan struct{}
}

func (p *ageProvider) Chat(_ context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, opts map[string]any) (*providers.LLMResponse, error) {
	if _, summary := opts[openai_compat.ResponseFormatJSONObjectOption]; summary {
		p.summaries <- struct{}{}
		return &providers.LLMResponse{Content: `{"version":2,"state":{"goals":[{"text":"weekly notes with Bob","refs":[{"seq_start":1,"seq_end":2}]}]},"covered_seq_start":0,"covered_seq_end":0}`, FinishReason: "stop"}, nil
	}
	return &providers.LLMResponse{Content: "hello", FinishReason: "stop"}, nil
}

func (p *ageProvider) GetDefaultModel() string { return "age-model" }

// TestDeferredCompaction_RealEngineAge: a person's message to a conversation
// whose oldest message is past the age trigger is answered without waiting
// for the summary and without the notice; the summary is written after the
// reply.
func TestDeferredCompaction_RealEngineAge(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	msgBus := bus.NewMessageBus()
	cfg := newTestConfig(t)
	cfg.Agents.List[0].Name = "Alice"
	provider := &ageProvider{summaries: make(chan struct{}, 4)}
	al := mustNewAgentLoop(t, cfg, msgBus, provider, nil)
	msg := inbound("c1", "id1", "hi")
	agent, sessionKey := inboundSessionKey(t, al, msg)

	// Seeded through the context manager, as turns write them, then
	// backdated: the oldest is past the default 7-day trigger.
	cm, release := al.getContextManager(agent, sessionKey)
	now := time.Now()
	ages := []int{10, 10, 4, 4}
	seeded := make([]memory.StoredMessage, 0, len(ages))
	for i, daysAgo := range ages {
		m := providers.Message{Role: "user", Content: fmt.Sprintf("Bob's weekly note %d", i)}
		add := cm.AddUserMessage
		if i%2 == 1 {
			m.Role, add = "assistant", cm.AddAssistantMessage
		}
		seq, err := add(context.Background(), m)
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		seeded = append(seeded, memory.StoredMessage{Seq: seq, CreatedAt: now.AddDate(0, 0, -daysAgo), Message: m})
	}
	release()
	store, ok := agent.Sessions.(*session.SQLiteStore)
	if !ok {
		t.Fatalf("agent session store is %T, want *session.SQLiteStore", agent.Sessions)
	}
	if err := store.SetHistoryWithSeqs(sessionKey, seeded); err != nil {
		t.Fatalf("backdate session: %v", err)
	}

	dispatch(al, msg)

	if out := nextOutbound(t, msgBus); out.Content != "hello" {
		t.Fatalf("first outbound = %+v, want the reply (no notice for an age-only compaction)", out)
	}
	select {
	case <-provider.summaries:
	case <-time.After(30 * time.Second):
		t.Fatal("the deferred age compaction never called the summary model")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, running := al.deferredCompactions.Load(agent.ID + ":" + sessionKey); !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the deferred age compaction never finished")
		}
		runtime.Gosched()
	}
	if agent.Sessions.GetSummary(sessionKey) == "" {
		t.Fatal("the deferred age compaction wrote no summary")
	}
	noOutboundExcept(t, msgBus, func(out bus.OutboundMessage) bool {
		// The engine's compaction report may follow; never the notice.
		return out.Content != compactionNoticeText("Alice")
	})
}
