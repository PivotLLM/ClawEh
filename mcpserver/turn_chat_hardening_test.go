// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// chatProbe records the turn chat and tool context a call over the MCP host
// runs with: what msg_send, msg_send_file, session_clear and agent_spawn
// each decide on.
type chatProbe struct {
	rc              tools.ReplyChat
	problem         tools.ChatProblem
	channel, chatID string
}

func (p *chatProbe) Name() string               { return "probe" }
func (p *chatProbe) Description() string        { return "probe" }
func (p *chatProbe) Parameters() map[string]any { return map[string]any{} }
func (p *chatProbe) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	p.rc, p.problem = tools.TurnChat(ctx)
	p.channel, p.chatID = tools.ToolChannel(ctx), tools.ToolChatID(ctx)
	return tools.NewToolResult("ok")
}

// Every kind of call over the MCP host gets the turn chat the loop would
// give it, so each tool's refusal is the loop's: no turn, a service token,
// several turns, an ask, an internal turn, a chat turn.
func TestDispatch_TurnChatPerCallKind(t *testing.T) {
	for _, tt := range []struct {
		name        string
		turns       [][2]string
		service     bool
		want        tools.ChatProblem
		wantChannel string
		wantChatID  string
	}{
		{"no turn", nil, false, tools.ChatNone, "", ""},
		{"service token", [][2]string{{"telegram", "42"}}, true, tools.ChatNone, "", ""},
		{"several turns", [][2]string{{"telegram", "42"}, {"slack", "C1"}}, false, tools.ChatAmbiguous, "", ""},
		{"ask", [][2]string{{constants.AgentMessageChannel, "ask-1"}}, false, tools.ChatAgent, constants.AgentMessageChannel, "ask-1"},
		{"sub-agent", [][2]string{{constants.SubagentChannel, "task"}}, false, tools.ChatAgent, constants.SubagentChannel, "task"},
		{"cli", [][2]string{{"cli", "direct"}}, false, tools.ChatInternal, "cli", "direct"},
		{"chat", [][2]string{{"telegram", "42"}}, false, tools.ChatOK, "telegram", "42"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := newSessionTokenStore()
			tok := st.Issue("alice", "agent:alice:main", "/ws")
			if tt.service {
				tok = "SST" + strings.Repeat("cd", 32)
				st.RegisterService(tok, "alice", "/ws")
			}
			for _, turn := range tt.turns {
				defer st.BeginTurn("agent:alice:main", turn[0], turn[1])()
			}
			probe := &chatProbe{}
			out, isErr := dispatchToolCall(context.Background(), "probe", map[string]any{"session_token": tok},
				st, resolverFor(map[string]*tools.ToolRegistry{"alice": newRegistryWith(probe)}), nil, nil, nil, nil)
			if isErr {
				t.Fatalf("dispatch = %q", out)
			}
			if probe.problem != tt.want || probe.channel != tt.wantChannel || probe.chatID != tt.wantChatID {
				t.Fatalf("probe saw %v on %q/%q, want %v on %q/%q", probe.problem, probe.channel, probe.chatID, tt.want, tt.wantChannel, tt.wantChatID)
			}
		})
	}
}

// Turns are per session: a turn of bob's session never gives alice's call a
// chat, nor makes it ambiguous.
func TestSessionTokenStore_TurnsArePerSession(t *testing.T) {
	st := newSessionTokenStore()
	alice := st.Issue("alice", "agent:alice:main", "/ws")
	defer st.BeginTurn("agent:bob:main", "telegram", "7")()
	rec, _ := st.Resolve(alice)
	if rc, ok := st.ReplyChat(rec); ok {
		t.Fatalf("alice with only bob's turn: %+v, want none", rc)
	}
	defer st.BeginTurn("agent:alice:main", "slack", "C1")()
	if rc, ok := st.ReplyChat(rec); !ok || rc != (tools.ReplyChat{Channel: "slack", ChatID: "C1"}) {
		t.Fatalf("alice's turn: %+v %v", rc, ok)
	}
}

// Concurrent turns beginning and ending on several sessions while calls
// read their chat and tokens rotate: every call sees a consistent answer,
// and once every turn has ended no session has a chat. Run with -race.
func TestSessionTokenStore_ConcurrentTurns(t *testing.T) {
	st := newSessionTokenStore()
	sessions := []string{"agent:alice:main", "agent:bob:main"}
	for _, s := range sessions {
		st.Issue(strings.Split(s, ":")[1], s, "/ws")
	}
	var wg sync.WaitGroup
	for w := range 16 {
		wg.Go(func() {
			session := sessions[w%len(sessions)]
			agent := strings.Split(session, ":")[1]
			for i := range 200 {
				end := st.BeginTurn(session, "telegram", strconv.Itoa(w))
				tok := st.Issue(agent, session, "/ws")
				if rec, ok := st.Resolve(tok); ok {
					rc, inTurn := st.ReplyChat(rec)
					// This worker's own turn runs, so the session has one.
					if !inTurn || (!rc.Ambiguous && rc.Channel != "telegram") {
						t.Errorf("worker %d call %d: ReplyChat = %+v %v while its turn runs", w, i, rc, inTurn)
					}
				}
				end()
				end() // a second end does nothing
			}
		})
	}
	wg.Wait()
	for _, s := range sessions {
		tok := st.Issue(strings.Split(s, ":")[1], s, "/ws")
		rec, _ := st.Resolve(tok)
		if rc, ok := st.ReplyChat(rec); ok {
			t.Fatalf("%s after every turn ended: %+v, want none", s, rc)
		}
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	if len(st.turns) != 0 {
		t.Fatalf("turns left: %v", st.turns)
	}
}

// consumeInbound returns the next inbound message already on the bus, or
// false when there is none.
func consumeInbound(mb *bus.MessageBus) (bus.InboundMessage, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	return mb.ConsumeInbound(ctx)
}

// A completion with no text but an error carries the error; one with
// neither, a nil result or no bus injects nothing; a clone's goes to its
// source's main conversation; a closed bus is logged, not fatal.
func TestPublishMCPAsyncToLLM_Edges(t *testing.T) {
	dest := tools.ReplyChat{Channel: "telegram", ChatID: "42"}
	rec := sessionRecord{agentID: "clone-1", sessionKey: "agent:clone-1:main", homeID: "alice"}

	mb := bus.NewMessageBus()
	defer mb.Close()
	publishMCPAsyncToLLM(context.Background(), mb, rec, dest, "agent_spawn", &tools.ToolResult{Err: errors.New("task failed")}, 1)
	m, ok := consumeInbound(mb)
	if !ok || m.Content != "task failed" {
		t.Fatalf("error completion: %+v %v", m, ok)
	}
	if m.SessionKey != "agent:alice:main" || m.Metadata["preresolved_agent_id"] != "alice" {
		t.Fatalf("clone's completion went to %q / %q, want alice's main conversation", m.SessionKey, m.Metadata["preresolved_agent_id"])
	}

	publishMCPAsyncToLLM(context.Background(), mb, rec, dest, "agent_spawn", &tools.ToolResult{}, 1)
	publishMCPAsyncToLLM(context.Background(), mb, rec, dest, "agent_spawn", nil, 1)
	publishMCPAsyncToLLM(context.Background(), nil, rec, dest, "agent_spawn", &tools.ToolResult{ForLLM: "x"}, 1)
	if m, ok := consumeInbound(mb); ok {
		t.Fatalf("injected %+v, want nothing", m)
	}

	closed := bus.NewMessageBus()
	closed.Close()
	publishMCPAsyncToLLM(context.Background(), closed, rec, dest, "agent_spawn", &tools.ToolResult{ForLLM: "x"}, 1)
}

// consumeOutbound returns the next outbound message already on the bus.
func consumeOutbound(mb *bus.MessageBus) (bus.OutboundMessage, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	return mb.SubscribeOutbound(ctx)
}

// A "/tools on" breadcrumb goes only to a chat turn's chat: never to an
// internal or asked turn, a call without a chat, or with an empty line.
func TestPublishMCPToolActivity_Destinations(t *testing.T) {
	rec := sessionRecord{agentID: "alice"}
	mb := bus.NewMessageBus()
	defer mb.Close()
	for _, dest := range []tools.ReplyChat{
		{},
		{Ambiguous: true, Channel: "telegram", ChatID: "42"},
		{Channel: "cli", ChatID: "direct"},
		{Channel: constants.AgentMessageChannel, ChatID: "ask-1"},
	} {
		publishMCPToolActivity(context.Background(), mb, rec, dest, "file_read", "🔧 file_read")
	}
	publishMCPToolActivity(context.Background(), mb, rec, tools.ReplyChat{Channel: "telegram", ChatID: "42"}, "file_read", "")
	publishMCPToolActivity(context.Background(), nil, rec, tools.ReplyChat{Channel: "telegram", ChatID: "42"}, "file_read", "x")
	if m, ok := consumeOutbound(mb); ok {
		t.Fatalf("published %+v, want nothing", m)
	}
	publishMCPToolActivity(context.Background(), mb, rec, tools.ReplyChat{Channel: "telegram", ChatID: "42"}, "file_read", "🔧 file_read")
	if m, ok := consumeOutbound(mb); !ok || m.Channel != "telegram" || m.ChatID != "42" || m.Content != "🔧 file_read" {
		t.Fatalf("chat turn: %+v %v", m, ok)
	}
	closed := bus.NewMessageBus()
	closed.Close()
	publishMCPToolActivity(context.Background(), closed, rec, tools.ReplyChat{Channel: "telegram", ChatID: "42"}, "file_read", "x")
}

// ForUser output reaches only a chat turn's chat; an internal turn, a
// call without a chat, a silent result and a closed bus deliver nothing.
func TestPublishMCPForUser_Destinations(t *testing.T) {
	rec := sessionRecord{agentID: "alice"}
	mb := bus.NewMessageBus()
	defer mb.Close()
	out := &tools.ToolResult{ForUser: "for you"}
	for _, dest := range []tools.ReplyChat{{}, {Ambiguous: true}, {Channel: "recovery", ChatID: "x"}} {
		publishMCPForUser(context.Background(), mb, rec, dest, "t", out)
	}
	publishMCPForUser(context.Background(), mb, rec, tools.ReplyChat{Channel: "telegram", ChatID: "42"}, "t", &tools.ToolResult{ForUser: "x", Silent: true})
	publishMCPForUser(context.Background(), nil, rec, tools.ReplyChat{Channel: "telegram", ChatID: "42"}, "t", out)
	if m, ok := consumeOutbound(mb); ok {
		t.Fatalf("published %+v, want nothing", m)
	}
	closed := bus.NewMessageBus()
	closed.Close()
	publishMCPForUser(context.Background(), closed, rec, tools.ReplyChat{Channel: "telegram", ChatID: "42"}, "t", out)
}

// noChatReason names each case in the log.
func TestNoChatReason(t *testing.T) {
	for dest, want := range map[tools.ReplyChat]string{
		{}:                         "no_turn_running",
		{Ambiguous: true}:          "several_turns_running",
		{Channel: "agent_message"}: "turn_has_no_chat",
	} {
		if got := noChatReason(dest); got != want {
			t.Errorf("noChatReason(%+v) = %q, want %q", dest, got, want)
		}
	}
}

// recordingSpawner records the spawns it is asked for and acknowledges them.
type recordingSpawner struct{ reqs []global.SpawnRequest }

func (s *recordingSpawner) Spawn(_ context.Context, req global.SpawnRequest) (*global.Result, error) {
	s.reqs = append(s.reqs, req)
	return &global.Result{ForLLM: "started"}, nil
}

// agent_spawn over the MCP host: a background task is refused while the
// call can't be tied to one turn (its result would have no chat), a waited
// one still runs; with one chat turn the task returns to that turn's chat.
func TestDispatch_SpawnCallbackPerTurn(t *testing.T) {
	sp := &recordingSpawner{}
	reg := tools.NewToolRegistry()
	for _, tl := range tools.NamespacedProvider("agent", toolsagents.GlobalProvider).Build(tools.ToolDeps{AgentID: "alice", Spawn: sp}) {
		reg.Register(tl)
	}
	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws")
	call := func(mode string) (string, bool) {
		return dispatchToolCall(context.Background(), "agent_spawn",
			map[string]any{"session_token": tok, "task": "count the cats", "mode": mode},
			st, resolverFor(map[string]*tools.ToolRegistry{"alice": reg}), nil, nil, nil, nil)
	}

	endChat := st.BeginTurn("agent:alice:main", "telegram", "42")
	endAsk := st.BeginTurn("agent:alice:main", constants.AgentMessageChannel, "ask-1")
	if out, isErr := call("callback"); !isErr || !strings.Contains(out, "A background task can't be started now: this agent is answering more than one conversation.") {
		t.Fatalf("several turns, callback: %q (error %v), want the refusal", out, isErr)
	}
	if len(sp.reqs) != 0 {
		t.Fatalf("spawned %+v, want nothing", sp.reqs)
	}
	if out, isErr := call("wait"); isErr {
		t.Fatalf("several turns, wait: %q", out)
	}
	endAsk()
	if out, isErr := call("callback"); isErr {
		t.Fatalf("one chat turn, callback: %q", out)
	}
	endChat()
	if len(sp.reqs) != 2 {
		t.Fatalf("spawned %d, want 2", len(sp.reqs))
	}
	if last := sp.reqs[1]; last.Mode != global.SpawnCallback || last.Channel != "telegram" || last.ChatID != "42" {
		t.Fatalf("callback spawn %+v, want it to answer telegram/42", last)
	}
	if first := sp.reqs[0]; first.Channel != "" || first.ChatID != "" {
		t.Fatalf("waited spawn with several turns carried %s/%s, want no chat", first.Channel, first.ChatID)
	}
}
