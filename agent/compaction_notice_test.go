// ClawEh
// License: MIT

package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/PivotLLM/ctxengine"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

// TestCompactionNotice_WhoIsTold: only a turn answering a person in a chat
// they read is told, once, when a compaction it waits on starts.
func TestCompactionNotice_WhoIsTold(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	alice := &AgentInstance{ID: "alice", Name: "Alice"}
	person := processOptions{Channel: "test", ChatID: "c1", FromPerson: true}
	blocking := ctxengine.CompactionStart{SessionKey: "s1", Reason: ctxengine.CompactionReasonThreshold, Blocking: true}
	const want = "Alice is summarising the conversation so far; the reply may take a moment."

	for _, tc := range []struct {
		name   string
		ctx    func() context.Context
		info   ctxengine.CompactionStart
		starts int
		told   bool
	}{
		{name: "person", ctx: func() context.Context { return withCompactionNotice(context.Background(), alice, person) }, info: blocking, starts: 1, told: true},
		{name: "person, several passes", ctx: func() context.Context { return withCompactionNotice(context.Background(), alice, person) }, info: blocking, starts: 3, told: true},
		{name: "not blocking", ctx: func() context.Context { return withCompactionNotice(context.Background(), alice, person) }, info: ctxengine.CompactionStart{Reason: ctxengine.CompactionReasonAge}, starts: 1},
		{name: "no turn", ctx: context.Background, info: blocking, starts: 1},
		{name: "scheduled job", ctx: func() context.Context {
			return withCompactionNotice(context.Background(), alice, processOptions{Channel: "test", ChatID: "c1"})
		}, info: blocking, starts: 1},
		{name: "ask", ctx: func() context.Context {
			return withCompactionNotice(context.Background(), alice, processOptions{Channel: constants.AgentMessageChannel, ChatID: "ask-1", FromPerson: true})
		}, info: blocking, starts: 1},
		{name: "sub-agent", ctx: func() context.Context {
			return withCompactionNotice(context.Background(), alice, processOptions{Channel: constants.SubagentChannel, ChatID: "s1", FromPerson: true})
		}, info: blocking, starts: 1},
		{name: "internal channel", ctx: func() context.Context {
			return withCompactionNotice(context.Background(), alice, processOptions{Channel: "cli", ChatID: "direct", FromPerson: true})
		}, info: blocking, starts: 1},
		{name: "fresh temporary agent", ctx: func() context.Context {
			fresh := &AgentInstance{ID: "f1", Name: "Bob", Spec: agentreg.Spec{ID: "f1", Origin: agentreg.OriginTemp, Fresh: true}}
			return withCompactionNotice(context.Background(), fresh, person)
		}, info: blocking, starts: 1},
		{name: "sub-agent inside a person's turn", ctx: func() context.Context {
			outer := withCompactionNotice(context.Background(), alice, person)
			return withCompactionNotice(outer, alice, processOptions{Channel: constants.SubagentChannel, ChatID: "s1"})
		}, info: blocking, starts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgBus := bus.NewMessageBus()
			al := mustNewAgentLoop(t, newTestConfig(t), msgBus, &mockProvider{}, nil)
			ctx := tc.ctx()
			for range tc.starts {
				al.compactionStarted(ctx, tc.info)
			}
			if tc.told {
				out := nextOutbound(t, msgBus)
				if out.Channel != "test" || out.ChatID != "c1" || out.Content != want || out.Outcome != "" {
					t.Fatalf("notice = %+v, want %q to test/c1 with no outcome", out, want)
				}
			}
			noOutbound(t, msgBus)
		})
	}
}

// TestCompactionNotice_BeforeReply: a compaction a person's turn waits on
// sends the notice once, to the person's chat and before the reply; a
// scheduled job's turn sends none.
func TestCompactionNotice_BeforeReply(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	for _, tc := range []struct {
		name string
		msg  bus.InboundMessage
		told bool
	}{
		{name: "person", msg: inbound("c1", "id1", "hi"), told: true},
		{name: "scheduled job", msg: bus.InboundMessage{Channel: "test", ChatID: "c1", SenderID: "cron", Content: "hi", Internal: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgBus := bus.NewMessageBus()
			cfg := newTestConfig(t)
			cfg.Agents.List[0].Name = "Alice"
			al := mustNewAgentLoop(t, cfg, msgBus, &fixedProvider{resp: &providers.LLMResponse{Content: "hello"}}, nil)
			agent, sessionKey := inboundSessionKey(t, al, tc.msg)
			cm := newDueContextManager(nil)
			close(cm.release)
			// The engine calls the hook for each blocking pass; the turn's
			// add and its request build both start one here.
			cm.onAdd = func(ctx context.Context) {
				al.compactionStarted(ctx, ctxengine.CompactionStart{SessionKey: sessionKey, Reason: ctxengine.CompactionReasonThreshold, Blocking: true})
			}
			injectContextManager(al, agent.ID, sessionKey, cm)

			dispatch(al, tc.msg)

			out := nextOutbound(t, msgBus)
			if tc.told {
				if out.ChatID != "c1" || out.Content != "Alice is summarising the conversation so far; the reply may take a moment." {
					t.Fatalf("first outbound = %+v, want the notice", out)
				}
				out = nextOutbound(t, msgBus)
			}
			if out.ChatID != "c1" || out.Content != "hello" {
				t.Fatalf("outbound = %+v, want the reply", out)
			}
			noOutbound(t, msgBus)
		})
	}
}

// TestCompactionNotice_RealEngine: the context manager the loop builds has
// the start hook installed, so a compaction the engine runs while a person's
// turn adds its message tells them before the reply.
func TestCompactionNotice_RealEngine(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	msgBus := bus.NewMessageBus()
	cfg := newTestConfig(t)
	cfg.Agents.List[0].Name = "Alice"
	cfg.Agents.Defaults.ContextWindow = 4000
	al := mustNewAgentLoop(t, cfg, msgBus, &fixedProvider{resp: &providers.LLMResponse{Content: "hello"}}, nil)
	msg := inbound("c1", "id1", "hi")
	agent, sessionKey := inboundSessionKey(t, al, msg)
	// History past the compaction threshold, so adding the message compacts.
	for i := range 8 {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		addMessage(t, agent.Sessions, sessionKey, role, fmt.Sprintf("Bob said %d %s", i, strings.Repeat("word ", 300)))
	}

	dispatch(al, msg)

	var queued []bus.OutboundMessage
	noOutboundExcept(t, msgBus, func(out bus.OutboundMessage) bool {
		queued = append(queued, out)
		return true
	})
	var notices, replies int
	for _, out := range queued {
		switch out.Content {
		case "Alice is summarising the conversation so far; the reply may take a moment.":
			if replies > 0 {
				t.Fatal("the notice came after the reply")
			}
			notices++
		case "hello":
			replies++
		}
	}
	if notices != 1 || replies != 1 {
		t.Fatalf("notices = %d, replies = %d; want one of each", notices, replies)
	}
}
