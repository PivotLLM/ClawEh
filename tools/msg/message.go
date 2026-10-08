package msg

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/tools"
)

// SendCallback sends content to the chat and reports what became of it:
// nil once the channel delivered it, ErrQueued when it was queued but no
// outcome was known in time, or why it was not sent (a channels sentinel,
// such as channels.ErrRecipientOffline, wrapped).
type SendCallback func(ctx context.Context, channel, chatID, content string) error

// ErrQueued is a SendCallback result: the message was handed to the
// channel, but its delivery had not been reported when the send stopped
// waiting.
var ErrQueued = errors.New("queued for delivery")

type MessageTool struct {
	sendCallback SendCallback
	sentInRound  atomic.Bool // Tracks whether a message was sent in the current processing round
}

func NewMessageTool() *MessageTool {
	return &MessageTool{}
}

func (t *MessageTool) Name() string {
	return "msg_send"
}

func (t *MessageTool) Description() string {
	return "Send a message to the user in the current conversation. Use this when you want to say something to the user."
}

func (t *MessageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "The message content to send",
			},
		},
		"required": []string{"content"},
	}
}

// ResetSentInRound resets the per-round send tracker.
// Called by the agent loop at the start of each inbound message processing round.
func (t *MessageTool) ResetSentInRound() {
	t.sentInRound.Store(false)
}

// HasSentInRound returns true if the message tool sent a message during the current round.
func (t *MessageTool) HasSentInRound() bool {
	return t.sentInRound.Load()
}

func (t *MessageTool) SetSendCallback(callback SendCallback) {
	t.sendCallback = callback
}

func (t *MessageTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	content, ok := args["content"].(string)
	if !ok {
		return &tools.ToolResult{ForLLM: "content is required", IsError: true}
	}

	// Security: the target is always the session's own source channel/chat. The
	// model cannot choose where a message goes — any channel/chat_id in args is
	// ignored, so an agent cannot be coaxed into sending into another channel or
	// to another user's chat.
	channel := tools.ToolChannel(ctx)
	chatID := tools.ToolChatID(ctx)

	if channel == "" || chatID == "" {
		return &tools.ToolResult{ForLLM: "No target channel/chat specified", IsError: true}
	}
	// An asked turn's own "chat" is the ask, whose only output is the final
	// reply: a message sent there would reach no one.
	if channel == constants.AgentMessageChannel {
		return &tools.ToolResult{ForLLM: "msg_send needs a target in an asked turn", IsError: true}
	}

	if t.sendCallback == nil {
		return &tools.ToolResult{ForLLM: "Message sending not configured", IsError: true}
	}

	chat := chatLabel(channel, chatID)
	err := t.sendCallback(ctx, channel, chatID, content)
	if err != nil && !errors.Is(err, ErrQueued) {
		return &tools.ToolResult{ForLLM: notSentText(chat, err), IsError: true, Err: notSentError(err)}
	}

	t.sentInRound.Store(true)
	if flag := tools.RoundSentFlagFromCtx(ctx); flag != nil {
		flag.Store(true)
	}
	text := "Message delivered to " + chat + "."
	if err != nil {
		text = "Message queued for delivery to " + chat + "."
	}
	// Silent: user already received the message directly
	return &tools.ToolResult{ForLLM: text, Silent: true}
}

// chatLabel names a chat as channel:chat, without repeating the channel
// when the chat ID already starts with it ("device:<id>", "webui:<id>").
func chatLabel(channel, chatID string) string {
	if strings.HasPrefix(chatID, channel+":") {
		return chatID
	}
	return channel + ":" + chatID
}

// notSentError marks a send the channel declined because the recipient is
// unavailable as an expected refusal: the channel has already logged it, so
// the tool call is not logged again as a failure. Any other error is
// returned as is.
func notSentError(err error) error {
	for _, expected := range []error{
		channels.ErrUnknownChannel, channels.ErrNotRunning, channels.ErrRecipientOffline,
		channels.ErrRecipientNotFound, channels.ErrReceiveOnly,
	} {
		if errors.Is(err, expected) {
			return tools.Refusal(err)
		}
	}
	return err
}

// notSentText says why a message did not reach chat, from the channel's
// reason.
func notSentText(chat string, err error) string {
	switch {
	case errors.Is(err, channels.ErrUnknownChannel):
		return "Not sent: " + chat + " is not set up."
	case errors.Is(err, channels.ErrNotRunning):
		return "Not sent: " + chat + " is unavailable."
	case errors.Is(err, channels.ErrRecipientOffline):
		return "Not sent: " + chat + " is offline."
	case errors.Is(err, channels.ErrRecipientNotFound):
		return "Not sent: " + chat + " can't be reached."
	case errors.Is(err, channels.ErrReceiveOnly):
		return "Not sent: " + chat + " is receive-only."
	}
	return "Not sent: couldn't reach " + chat + "."
}
