// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/tools"
	toolsmsg "github.com/PivotLLM/ClawEh/tools/msg"
)

// sentMessage is one msg_send delivery.
type sentMessage struct{ channel, chatID, content string }

// turnChatHost is alice's registry with msg_send (recording where it sends)
// and a tool with user-facing output, alice's conversation and service
// tokens, and a bus collecting outbound messages.
type turnChatHost struct {
	st       *SessionTokenStore
	tok, svc string
	reg      *tools.ToolRegistry
	sent     chan sentMessage
	msgBus   *bus.MessageBus
	outbound chan bus.OutboundMessage
}

func newTurnChatHost(t *testing.T) *turnChatHost {
	t.Helper()
	h := &turnChatHost{
		st:       newSessionTokenStore(),
		sent:     make(chan sentMessage, 8),
		msgBus:   bus.NewMessageBus(),
		outbound: make(chan bus.OutboundMessage, 8),
	}
	t.Cleanup(h.msgBus.Close)
	h.tok = h.st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	h.svc = "SST" + strings.Repeat("ef", 32)
	h.st.RegisterService(h.svc, "alice", "/ws/alice/sessions")
	send := toolsmsg.NewMessageTool()
	send.SetSendCallback(func(_ context.Context, channel, chatID, content string) error {
		h.sent <- sentMessage{channel, chatID, content}
		return nil
	})
	notice := &mockTool{name: "notice", params: map[string]any{}, result: &tools.ToolResult{ForLLM: "ok", ForUser: "for the user"}}
	h.reg = newRegistryWith(send, notice)
	go func() {
		for {
			out, ok := h.msgBus.SubscribeOutbound(t.Context())
			if !ok {
				return
			}
			h.outbound <- out
		}
	}()
	return h
}

func (h *turnChatHost) call(token, tool string) (string, bool) {
	args := map[string]any{"session_token": token}
	if tool == "msg_send" {
		args["content"] = "hello"
	}
	return dispatchToolCall(context.Background(), tool, args, h.st,
		resolverFor(map[string]*tools.ToolRegistry{"alice": h.reg}), nil, nil, h.msgBus, nil)
}

// expectSent fails unless msg_send delivered to channel/chatID.
func (h *turnChatHost) expectSent(t *testing.T, channel, chatID string) {
	t.Helper()
	select {
	case m := <-h.sent:
		if m.channel != channel || m.chatID != chatID {
			t.Fatalf("msg_send went to %s/%s, want %s/%s", m.channel, m.chatID, channel, chatID)
		}
	default:
		t.Fatalf("msg_send sent nothing, want %s/%s", channel, chatID)
	}
}

// expectOutbound fails unless the next outbound (ForUser) went to channel/chatID.
func (h *turnChatHost) expectOutbound(t *testing.T, channel, chatID string) {
	t.Helper()
	select {
	case out := <-h.outbound:
		if out.Channel != channel || out.ChatID != chatID {
			t.Fatalf("ForUser went to %s/%s, want %s/%s", out.Channel, out.ChatID, channel, chatID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no ForUser published, want %s/%s", channel, chatID)
	}
}

// expectNothing fails if anything was sent or published.
func (h *turnChatHost) expectNothing(t *testing.T) {
	t.Helper()
	select {
	case m := <-h.sent:
		t.Fatalf("msg_send sent %+v, want nothing", m)
	case out := <-h.outbound:
		t.Fatalf("published %+v, want nothing", out)
	case <-time.After(150 * time.Millisecond):
	}
}

// An ask running beside a chat turn of the same agent: a call with the
// conversation token can't be tied to either, so msg_send is refused and
// user-facing output is dropped, never delivered to the chat; once the ask
// ends, the chat turn's output goes to its chat, and the ask's never does.
func TestDispatch_MsgSendConcurrentAskAndChatTurn(t *testing.T) {
	h := newTurnChatHost(t)
	endChat := h.st.BeginTurn("agent:alice:main", "telegram", "42")
	endAsk := h.st.BeginTurn("agent:alice:main", constants.AgentMessageChannel, "")

	if out, isErr := h.call(h.tok, "msg_send"); !isErr || out != "Messages can't be sent now: this agent is answering more than one conversation." {
		t.Fatalf("both turns: msg_send = %q (error %v), want the refusal", out, isErr)
	}
	if out, isErr := h.call(h.tok, "notice"); isErr || out != "ok" {
		t.Fatalf("both turns: notice = %q (error %v)", out, isErr)
	}
	h.expectNothing(t)

	endChat()
	if out, isErr := h.call(h.tok, "msg_send"); !isErr || !strings.Contains(out, "another agent") {
		t.Fatalf("ask alone: msg_send = %q (error %v), want the refusal", out, isErr)
	}
	h.call(h.tok, "notice")
	h.expectNothing(t)
	endAsk()

	endChat = h.st.BeginTurn("agent:alice:main", "telegram", "42")
	defer endChat()
	if out, isErr := h.call(h.tok, "msg_send"); isErr {
		t.Fatalf("chat turn alone: msg_send = %q", out)
	}
	h.expectSent(t, "telegram", "42")
	h.call(h.tok, "notice")
	h.expectOutbound(t, "telegram", "42")
}

// A claw agent (cli) turn over the MCP host: msg_send is refused and
// user-facing output is dropped, never sent to the chat an earlier turn
// answered.
func TestDispatch_MsgSendCLITurn(t *testing.T) {
	h := newTurnChatHost(t)
	h.st.BeginTurn("agent:alice:main", "telegram", "42")() // an earlier chat turn
	defer h.st.BeginTurn("agent:alice:main", "cli", "direct")()
	if out, isErr := h.call(h.tok, "msg_send"); !isErr || out != "This chat can't receive messages." {
		t.Fatalf("msg_send = %q (error %v), want the refusal", out, isErr)
	}
	h.call(h.tok, "notice")
	h.expectNothing(t)
}

// A service token belongs to no turn: msg_send is refused and user-facing
// output dropped even while a chat turn of the session runs.
func TestDispatch_MsgSendServiceToken(t *testing.T) {
	h := newTurnChatHost(t)
	defer h.st.BeginTurn("agent:alice:main", "telegram", "42")()
	if out, isErr := h.call(h.svc, "msg_send"); !isErr || out != "There is no chat to send messages to." {
		t.Fatalf("msg_send = %q (error %v), want the refusal", out, isErr)
	}
	h.call(h.svc, "notice")
	h.expectNothing(t)
}

// A tool result's (system) turn and a scheduled job's turn answer a chat
// (the loop records it as the turn's chat): output goes there.
func TestDispatch_MsgSendTurnAnsweringAChat(t *testing.T) {
	h := newTurnChatHost(t)
	h.st.BeginTurn("agent:alice:main", "telegram", "42")() // an earlier chat turn
	defer h.st.BeginTurn("agent:alice:main", "slack", "C9")()
	if out, isErr := h.call(h.tok, "msg_send"); isErr {
		t.Fatalf("msg_send = %q", out)
	}
	h.expectSent(t, "slack", "C9")
	h.call(h.tok, "notice")
	h.expectOutbound(t, "slack", "C9")
}

// Parity: for every kind of turn, msg_send over the MCP host says and does
// exactly what it does in the agent loop, which runs a tool with the turn's
// chat as its reply chat and tool context (agent/tool_exec.go runToolCall).
func TestDispatch_MsgSendParityWithLoop(t *testing.T) {
	for _, rc := range []tools.ReplyChat{
		{Channel: "telegram", ChatID: "42"},
		{Channel: "cli", ChatID: "direct"},
		{Channel: "recovery", ChatID: "x"},
		{Channel: constants.AgentMessageChannel},
		{Channel: constants.SubagentChannel, ChatID: "agent:alice:task"},
	} {
		t.Run(rc.Channel, func(t *testing.T) {
			h := newTurnChatHost(t)
			ctx := tools.WithReplyChat(context.Background(), rc)
			inLoop := h.reg.ExecuteWithContext(ctx, "msg_send", map[string]any{"content": "hello"}, rc.Channel, rc.ChatID, nil)
			var loopSent *sentMessage
			select {
			case m := <-h.sent:
				loopSent = &m
			default:
			}

			end := h.st.BeginTurn("agent:alice:main", rc.Channel, rc.ChatID)
			defer end()
			out, isErr := h.call(h.tok, "msg_send")
			var mcpSent *sentMessage
			select {
			case m := <-h.sent:
				mcpSent = &m
			default:
			}

			if out != inLoop.ForLLM || isErr != inLoop.IsError {
				t.Fatalf("MCP = %q (error %v), loop = %q (error %v)", out, isErr, inLoop.ForLLM, inLoop.IsError)
			}
			if (loopSent == nil) != (mcpSent == nil) || (loopSent != nil && *loopSent != *mcpSent) {
				t.Fatalf("MCP sent %+v, loop sent %+v", mcpSent, loopSent)
			}
		})
	}
}
