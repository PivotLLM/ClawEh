// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// fixedProvider answers every Chat call with the same response and error.
type fixedProvider struct {
	resp *providers.LLMResponse
	err  error
}

func (p *fixedProvider) Chat(context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]any) (*providers.LLMResponse, error) {
	if p.err != nil {
		return nil, p.err
	}
	r := *p.resp
	return &r, nil
}

func (p *fixedProvider) GetDefaultModel() string { return "mock-model" }

// noOutbound fails the test if the loop publishes anything within a short wait.
func noOutbound(t *testing.T, msgBus *bus.MessageBus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if out, ok := msgBus.SubscribeOutbound(ctx); ok {
		t.Fatalf("unexpected outbound %+v", out)
	}
}

// required marks msg as requiring exactly one final reply.
func required(msg bus.InboundMessage) bus.InboundMessage {
	if msg.Metadata == nil {
		msg.Metadata = map[string]string{}
	}
	msg.Metadata[bus.MetaReplyRequired] = "1"
	return msg
}

// TestRunTurn_ReplyOutcome: the final reply of a turn carries its outcome on
// every path, with and without reply_required; the flag only changes what is
// sent for an empty reply.
func TestRunTurn_ReplyOutcome(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	for _, tc := range []struct {
		name        string
		provider    *fixedProvider
		group       bool
		flag        bool
		wantSent    bool
		wantOutcome string
		wantContent string // exact when wantExact, else a substring
		wantExact   bool
	}{
		{
			name: "ok", provider: &fixedProvider{resp: &providers.LLMResponse{Content: "hello"}},
			wantSent: true, wantOutcome: bus.OutcomeOK, wantContent: "hello", wantExact: true,
		},
		{
			name: "ok required", provider: &fixedProvider{resp: &providers.LLMResponse{Content: "hello"}},
			flag: true, wantSent: true, wantOutcome: bus.OutcomeOK, wantContent: "hello", wantExact: true,
		},
		{
			name: "empty direct", provider: &fixedProvider{resp: &providers.LLMResponse{FinishReason: "stop", Normal: true}},
			wantSent: true, wantOutcome: bus.OutcomeEmpty, wantContent: "empty response",
		},
		{
			name: "empty direct required", provider: &fixedProvider{resp: &providers.LLMResponse{FinishReason: "stop", Normal: true}},
			flag: true, wantSent: true, wantOutcome: bus.OutcomeEmpty, wantContent: "", wantExact: true,
		},
		{
			name: "empty group", provider: &fixedProvider{resp: &providers.LLMResponse{FinishReason: "stop", Normal: true}},
			group: true, wantSent: false,
		},
		{
			name: "empty group required", provider: &fixedProvider{resp: &providers.LLMResponse{FinishReason: "stop", Normal: true}},
			group: true, flag: true, wantSent: true, wantOutcome: bus.OutcomeEmpty, wantContent: "", wantExact: true,
		},
		{
			name: "no-response sentinel required", provider: &fixedProvider{resp: &providers.LLMResponse{Content: noResponseSentinel}},
			flag: true, wantSent: true, wantOutcome: bus.OutcomeEmpty, wantContent: "", wantExact: true,
		},
		{
			name: "abnormal empty", provider: &fixedProvider{resp: &providers.LLMResponse{FinishReason: "length"}},
			wantSent: true, wantOutcome: bus.OutcomeError, wantContent: "finish reason: length",
		},
		{
			name: "abnormal empty required", provider: &fixedProvider{resp: &providers.LLMResponse{FinishReason: "length"}},
			flag: true, wantSent: true, wantOutcome: bus.OutcomeError, wantContent: "finish reason: length",
		},
		{
			name: "error", provider: &fixedProvider{err: errors.New("boom")},
			wantSent: true, wantOutcome: bus.OutcomeError, wantContent: "boom",
		},
		{
			name: "error required", provider: &fixedProvider{err: errors.New("boom")},
			flag: true, wantSent: true, wantOutcome: bus.OutcomeError, wantContent: "boom",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgBus := bus.NewMessageBus()
			al := mustNewAgentLoop(t, newTestConfig(t), msgBus, tc.provider, nil)
			msg := inbound("c1", "id1", "hi")
			if tc.group {
				msg.Metadata = map[string]string{"is_group": "true"}
			}
			if tc.flag {
				msg = required(msg)
			}
			dispatch(al, msg)

			if !tc.wantSent {
				noOutbound(t, msgBus)
				return
			}
			out := nextOutbound(t, msgBus)
			if out.Outcome != tc.wantOutcome || out.OriginalMessageID != "id1" || out.ChatID != "c1" {
				t.Fatalf("reply = %+v, want outcome %q to id1 in c1", out, tc.wantOutcome)
			}
			if tc.wantExact && out.Content != tc.wantContent {
				t.Fatalf("content = %q, want %q", out.Content, tc.wantContent)
			}
			if !tc.wantExact && !strings.Contains(out.Content, tc.wantContent) {
				t.Fatalf("content = %q, want it to contain %q", out.Content, tc.wantContent)
			}
			noOutbound(t, msgBus)
		})
	}
}

// TestRunTurn_AgentGone: a message addressed to an agent that does not exist
// is dropped silently, unless the sender requires a reply: then it gets an
// error outcome.
func TestRunTurn_AgentGone(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	for _, flag := range []bool{false, true} {
		msgBus := bus.NewMessageBus()
		al := mustNewAgentLoop(t, newTestConfig(t), msgBus, &mockProvider{}, nil)
		msg := inbound("c1", "id1", "hi")
		msg.Metadata = map[string]string{metadataKeyPreresolvedAgentID: "no-such-agent"}
		if flag {
			msg = required(msg)
		}
		dispatch(al, msg)
		if !flag {
			noOutbound(t, msgBus)
			continue
		}
		if out := nextOutbound(t, msgBus); out.Outcome != bus.OutcomeError || out.OriginalMessageID != "id1" {
			t.Fatalf("reply = %+v, want an error outcome to id1", out)
		}
	}
}

// roundSendingTool marks the round as already replied to, as msg_send does.
type roundSendingTool struct{}

func (roundSendingTool) Name() string               { return "fake_send" }
func (roundSendingTool) Description() string        { return "marks the round sent" }
func (roundSendingTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (roundSendingTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	if f := tools.RoundSentFlagFromCtx(ctx); f != nil {
		f.Store(true)
	}
	return &tools.ToolResult{ForLLM: "sent", Silent: true}
}

// TestRunTurn_MsgSendSuppressesUnlessRequired: after msg_send replied in the
// turn the final reply is suppressed, unless the sender requires it.
func TestRunTurn_MsgSendSuppressesUnlessRequired(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	for _, flag := range []bool{false, true} {
		msgBus := bus.NewMessageBus()
		cfg := newTestConfig(t)
		cfg.Agents.List[0].Tools = []string{"*"}
		provider := &sequenceProvider{
			responses: []*providers.LLMResponse{
				{ToolCalls: []providers.ToolCall{toolCallTo("tc-1", "fake_send")}},
				{Content: "final"},
			},
			errors: []error{nil, nil},
		}
		al := mustNewAgentLoop(t, cfg, msgBus, provider, nil)
		al.RegisterTool(roundSendingTool{})
		msg := inbound("c1", "id1", "hi")
		if flag {
			msg = required(msg)
		}
		dispatch(al, msg)
		if !flag {
			noOutbound(t, msgBus)
			continue
		}
		out := nextOutbound(t, msgBus)
		if out.Outcome != bus.OutcomeOK || out.Content != "final" {
			t.Fatalf("required reply = %+v, want ok \"final\"", out)
		}
		noOutbound(t, msgBus)
	}
}

// TestInbound_CancelOutcomes: /cancel gives the cancelled running turn a
// cancelled outcome, and a queued message that requires a reply gets its own
// cancelled reply; a queued message without the flag still gets nothing.
func TestInbound_CancelOutcomes(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al, msgBus, p := newBlockingLoop(t, newTestConfig(t))

	go dispatch(al, required(inbound("c1", "id1", "m1")))
	p.waitStarted(t)
	dispatch(al, required(inbound("c2", "id2", "m2"))) // queued, requires a reply
	dispatch(al, inbound("c3", "id3", "m3"))           // queued, no flag
	dispatch(al, inbound("c4", "id4", "/cancel"))

	got := map[string]bus.OutboundMessage{}
	for range 3 {
		out := nextOutbound(t, msgBus)
		got[out.OriginalMessageID] = out
	}
	al.activeRequests.Wait()
	noOutbound(t, msgBus)

	if out := got["id1"]; out.Outcome != bus.OutcomeCancelled || !strings.Contains(out.Content, "Cancelled by /cancel") {
		t.Fatalf("running turn reply = %+v, want cancelled", out)
	}
	if out := got["id2"]; out.Outcome != bus.OutcomeCancelled || out.ChatID != "c2" {
		t.Fatalf("queued required reply = %+v, want cancelled in c2", out)
	}
	if out := got["id4"]; out.Outcome != bus.OutcomeOK || !strings.Contains(out.Content, "Cancelled the current request") {
		t.Fatalf("/cancel reply = %+v, want its ok report", out)
	}
	if _, ok := got["id3"]; ok {
		t.Fatal("queued message without reply_required got a reply")
	}
}

// TestRunTurn_ShutdownSendsNothing: a turn interrupted by shutdown sends
// nothing, even when the sender requires a reply.
func TestRunTurn_ShutdownSendsNothing(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al, msgBus, p := newBlockingLoop(t, newTestConfig(t))
	parent, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	go func() {
		al.runTurn(context.Background(), parent, required(inbound("c1", "id1", "m1")))
		close(done)
	}()
	p.waitStarted(t)
	cancel(errShuttingDown)
	<-done
	noOutbound(t, msgBus)
}

// TestInbound_DistinctChatIDsSameAgentNotMerged: two messages for one agent on
// the same channel with different chat ids (one per forum attempt) run as two
// turns with a reply each, never merged.
func TestInbound_DistinctChatIDsSameAgentNotMerged(t *testing.T) {
	al, msgBus, p := newBlockingLoop(t, newTestConfig(t))
	msg := func(chat, id, content string) bus.InboundMessage {
		m := required(bus.InboundMessage{Channel: "forum", ChatID: chat, SenderID: "forum", MessageID: id, Content: content})
		m.Metadata[metadataKeyPreresolvedAgentID] = "main"
		return m
	}

	go dispatch(al, msg("f/w1/1", "a", "first"))
	p.waitStarted(t)
	dispatch(al, msg("f/w2/1", "b", "second"))
	dispatch(al, msg("f/w3/1", "c", "third"))

	replies := map[string]string{}
	for i := range 3 {
		p.release <- struct{}{}
		out := nextOutbound(t, msgBus)
		replies[out.ChatID] = out.OriginalMessageID
		if i < 2 {
			p.waitStarted(t)
		}
	}
	al.activeRequests.Wait()

	if n := p.callCount(); n != 3 {
		t.Fatalf("provider calls = %d, want 3 (one per chat id)", n)
	}
	want := map[string]string{"f/w1/1": "a", "f/w2/1": "b", "f/w3/1": "c"}
	for chat, id := range want {
		if replies[chat] != id {
			t.Fatalf("replies = %v, want %v", replies, want)
		}
	}
}

// TestWithInboundSpawnDepth: the inbound spawn depth only ever raises the
// context's depth, is clamped to the maximum (3 here); invalid values are
// ignored.
func TestWithInboundSpawnDepth(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	for _, tc := range []struct {
		name  string
		start int
		value string
		want  int
	}{
		{name: "unset", start: 0, value: "", want: 0},
		{name: "raises", start: 0, value: "3", want: 3},
		{name: "raises above own", start: 1, value: " 2 ", want: 2},
		{name: "never lowers", start: 2, value: "1", want: 2},
		{name: "equal", start: 2, value: "2", want: 2},
		{name: "not a number", start: 1, value: "abc", want: 1},
		{name: "negative", start: 0, value: "-1", want: 0},
		{name: "clamped to max", start: 0, value: "99", want: 3},
		{name: "clamp never lowers", start: 4, value: "99", want: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := toolsagents.WithSpawnDepth(context.Background(), tc.start)
			msg := bus.InboundMessage{}
			if tc.value != "" {
				msg.Metadata = map[string]string{bus.MetaSpawnDepth: tc.value}
			}
			if got := toolsagents.SpawnDepth(withInboundSpawnDepth(ctx, msg, 3)); got != tc.want {
				t.Fatalf("depth = %d, want %d", got, tc.want)
			}
		})
	}
}

// depthTool records the sub-agent depth its call runs at.
type depthTool struct{ depth *atomic.Int64 }

func (depthTool) Name() string               { return "depth_probe" }
func (depthTool) Description() string        { return "records the spawn depth" }
func (depthTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (d depthTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	d.depth.Store(int64(toolsagents.SpawnDepth(ctx)))
	return &tools.ToolResult{ForLLM: "ok", Silent: true}
}

// TestInbound_SpawnDepthReachesTools: a bus turn's tools run at the depth the
// sender set, and at depth 0 without it.
func TestInbound_SpawnDepthReachesTools(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	for _, tc := range []struct {
		value string
		want  int64
	}{{"", 0}, {"3", 3}} {
		cfg := newTestConfig(t)
		cfg.Agents.List[0].Tools = []string{"*"}
		provider := &sequenceProvider{
			responses: []*providers.LLMResponse{
				{ToolCalls: []providers.ToolCall{toolCallTo("tc-1", "depth_probe")}},
				{Content: "done"},
			},
			errors: []error{nil, nil},
		}
		msgBus := bus.NewMessageBus()
		al := mustNewAgentLoop(t, cfg, msgBus, provider, nil)
		var depth atomic.Int64
		depth.Store(-1)
		al.RegisterTool(depthTool{depth: &depth})
		msg := inbound("c1", "id1", "go")
		if tc.value != "" {
			msg.Metadata = map[string]string{bus.MetaSpawnDepth: tc.value}
		}
		dispatch(al, msg)
		nextOutbound(t, msgBus)
		if got := depth.Load(); got != tc.want {
			t.Fatalf("spawn_depth %q: tool ran at depth %d, want %d", tc.value, got, tc.want)
		}
	}
}

// TestProcessSystemMessage_NoOutcome: the SendResponse path (an async result
// delivered to its origin chat) answers no inbound message, so it carries no
// outcome.
func TestProcessSystemMessage_NoOutcome(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	msgBus := bus.NewMessageBus()
	al := mustNewAgentLoop(t, newTestConfig(t), msgBus, &mockProvider{}, nil)
	if _, err := al.processSystemMessage(context.Background(), bus.InboundMessage{
		Channel: "system", SenderID: "async:agent_spawn", ChatID: "telegram:chat-1", Content: "Result:\nfine",
	}, nil); err != nil {
		t.Fatalf("processSystemMessage: %v", err)
	}
	out := nextOutbound(t, msgBus)
	if out.Channel != "telegram" || out.ChatID != "chat-1" || out.Outcome != "" {
		t.Fatalf("reply = %+v, want a reply to telegram:chat-1 with no outcome", out)
	}
}

// TestInbound_CancelWhileWaitingForSlot: a turn cancelled while it waits for a
// concurrent-turn slot never runs; if its sender requires a reply it gets a
// cancelled one.
func TestInbound_CancelWhileWaitingForSlot(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al, msgBus, p := newBlockingLoop(t, twoAgentConfig(t, 1))

	go dispatch(al, bus.InboundMessage{Channel: "cha", ChatID: "x", SenderID: "u", MessageID: "a1", Content: "first"})
	p.waitStarted(t)
	go dispatch(al, required(bus.InboundMessage{Channel: "chb", ChatID: "y", SenderID: "u", MessageID: "b1", Content: "second"}))
	time.Sleep(100 * time.Millisecond) // b's turn is now waiting for the slot
	dispatch(al, bus.InboundMessage{Channel: "chb", ChatID: "y", SenderID: "u", MessageID: "b2", Content: "/cancel"})

	got := map[string]bus.OutboundMessage{}
	for range 2 {
		out := nextOutbound(t, msgBus)
		got[out.OriginalMessageID] = out
	}
	if out := got["b1"]; out.Outcome != bus.OutcomeCancelled {
		t.Fatalf("waiting turn reply = %+v, want cancelled (got %v)", out, got)
	}
	p.release <- struct{}{}
	nextOutbound(t, msgBus)
	al.activeRequests.Wait()
	if n := p.callCount(); n != 1 {
		t.Fatalf("provider calls = %d, want 1 (the cancelled turn never ran)", n)
	}
}

// TestInbound_ReplyRequiredNeverMerged: messages that require a reply are
// never merged, even from the same chat: two flagged messages queued behind
// a busy session each get exactly one final reply addressed to themselves,
// and a flagged and an unflagged message from one chat run as separate turns.
func TestInbound_ReplyRequiredNeverMerged(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	al, msgBus, p := newBlockingLoop(t, newTestConfig(t))

	go dispatch(al, inbound("c1", "id1", "m1"))
	p.waitStarted(t)
	dispatch(al, required(inbound("c1", "id2", "m2")))
	dispatch(al, required(inbound("c1", "id3", "m3")))
	dispatch(al, inbound("c1", "id4", "m4"))
	dispatch(al, required(inbound("c1", "id5", "m5")))

	replies := map[string]int{}
	for i := range 5 {
		p.release <- struct{}{}
		out := nextOutbound(t, msgBus)
		replies[out.OriginalMessageID]++
		if i < 4 {
			p.waitStarted(t)
		}
	}
	al.activeRequests.Wait()
	noOutbound(t, msgBus)

	if n := p.callCount(); n != 5 {
		t.Fatalf("provider calls = %d, want 5 (no merging around flagged messages)", n)
	}
	for _, id := range []string{"id1", "id2", "id3", "id4", "id5"} {
		if replies[id] != 1 {
			t.Fatalf("replies = %v, want exactly one per message", replies)
		}
	}
	for i, want := range []string{"m1", "m2", "m3", "m4", "m5"} {
		if got := p.call(i); !strings.HasSuffix(got, want) || strings.Contains(got, "\n") {
			t.Fatalf("turn %d content = %q, want %q alone", i, got, want)
		}
	}
}

// asyncProbeTool completes in the background, handing its callback a result.
type asyncProbeTool struct{}

func (asyncProbeTool) Name() string               { return "async_probe" }
func (asyncProbeTool) Description() string        { return "completes later" }
func (asyncProbeTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (asyncProbeTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	return &tools.ToolResult{ForLLM: "sync"}
}

func (asyncProbeTool) ExecuteAsync(ctx context.Context, _ map[string]any, cb tools.AsyncCallback) *tools.ToolResult {
	go cb(context.WithoutCancel(ctx), &tools.ToolResult{ForLLM: "late result", Silent: true})
	return &tools.ToolResult{ForLLM: "started", Async: true}
}

// TestAsyncResult_CarriesTurnDepth: an async tool result re-injected into the
// agent carries the spawning turn's depth, so the re-entered turn cannot run
// lower; a depth-0 turn adds none.
func TestAsyncResult_CarriesTurnDepth(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	for _, tc := range []struct{ value, want string }{{"", ""}, {"2", "2"}} {
		cfg := newTestConfig(t)
		cfg.Agents.List[0].Tools = []string{"*"}
		provider := &sequenceProvider{
			responses: []*providers.LLMResponse{
				{ToolCalls: []providers.ToolCall{toolCallTo("tc-1", "async_probe")}},
				{Content: "done"},
			},
			errors: []error{nil, nil},
		}
		msgBus := bus.NewMessageBus()
		al := mustNewAgentLoop(t, cfg, msgBus, provider, nil)
		al.RegisterTool(asyncProbeTool{})
		msg := inbound("c1", "id1", "go")
		if tc.value != "" {
			msg.Metadata = map[string]string{bus.MetaSpawnDepth: tc.value}
		}
		dispatch(al, msg)
		nextOutbound(t, msgBus)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		in, ok := msgBus.ConsumeInbound(ctx)
		cancel()
		if !ok || in.Channel != "system" {
			t.Fatalf("spawn_depth %q: no re-injected system message (%+v)", tc.value, in)
		}
		if got := in.Metadata[bus.MetaSpawnDepth]; got != tc.want {
			t.Fatalf("spawn_depth %q: re-injected depth = %q, want %q", tc.value, got, tc.want)
		}
	}
}
