package tools

import (
	"context"

	"github.com/PivotLLM/ClawEh/constants"
)

// ReplyChat is where the final reply of the turn a tool call belongs to
// goes: the chat a tool's output for "the current chat" is sent to
// (msg_send, msg_send_file, a tool's ForUser text, a background result).
// The agent loop sets the turn's own chat; the MCP host sets the chat of the
// one turn running on the call's session token.
type ReplyChat struct {
	Channel string
	ChatID  string
	// Ambiguous marks a call that cannot be tied to one turn: over the MCP
	// host, a conversation token while more than one turn of its session
	// runs.
	Ambiguous bool
}

// Target returns the channel and chat of rc; both empty when rc is
// Ambiguous, so nothing is sent to either turn's chat.
func (rc ReplyChat) Target() (channel, chatID string) {
	if rc.Ambiguous {
		return "", ""
	}
	return rc.Channel, rc.ChatID
}

type replyChatKey struct{}

// WithReplyChat returns ctx carrying the reply chat of the call's turn.
func WithReplyChat(ctx context.Context, rc ReplyChat) context.Context {
	return context.WithValue(ctx, replyChatKey{}, rc)
}

// ReplyChatOf returns the reply chat of the call's turn; false when the call
// belongs to no turn (a service token, a call outside a turn).
func ReplyChatOf(ctx context.Context) (ReplyChat, bool) {
	rc, ok := ctx.Value(replyChatKey{}).(ReplyChat)
	return rc, ok
}

// ChatProblem says why a call cannot send to the chat of its turn.
type ChatProblem int

const (
	// ChatOK is a chat output can be sent to.
	ChatOK ChatProblem = iota
	// ChatNone is a call that belongs to no turn.
	ChatNone
	// ChatAmbiguous is a call that cannot be tied to one of several turns.
	ChatAmbiguous
	// ChatAgent is an asked or sub-agent turn: its reply goes to an agent.
	ChatAgent
	// ChatInternal is a turn with no chat a person reads (cli, system,
	// recovery).
	ChatInternal
)

// TurnChat returns the chat of the call's turn and whether output can be
// sent there. It is the one rule every tool that sends to "the current chat"
// applies, in the agent loop and over the MCP host alike.
func TurnChat(ctx context.Context) (ReplyChat, ChatProblem) {
	rc, ok := ReplyChatOf(ctx)
	switch {
	case !ok:
		return rc, ChatNone
	case rc.Ambiguous:
		return rc, ChatAmbiguous
	case constants.RepliesToAgent(rc.Channel):
		return rc, ChatAgent
	case rc.Channel == "" || rc.ChatID == "" || constants.IsInternalChannel(rc.Channel):
		return rc, ChatInternal
	}
	return rc, ChatOK
}
