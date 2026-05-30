package msg

import (
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"context"
	"fmt"
	"sync/atomic"
)

type SendCallback func(channel, chatID, content string) error

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
	return "Send a message to user on a chat channel. Use this when you want to communicate something."
}

func (t *MessageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "The message content to send",
			},
			"channel": map[string]any{
				"type":        "string",
				"description": "Optional: target channel (telegram, slack, etc.)",
			},
			"chat_id": map[string]any{
				"type":        "string",
				"description": "Optional: target chat/user ID",
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

	channel, _ := args["channel"].(string)
	chatID, _ := args["chat_id"].(string)

	if channel == "" {
		channel = tools.ToolChannel(ctx)
	}
	if chatID == "" {
		chatID = tools.ToolChatID(ctx)
	}

	if channel == "" || chatID == "" {
		return &tools.ToolResult{ForLLM: "No target channel/chat specified", IsError: true}
	}

	if t.sendCallback == nil {
		return &tools.ToolResult{ForLLM: "Message sending not configured", IsError: true}
	}

	if err := t.sendCallback(channel, chatID, content); err != nil {
		return &tools.ToolResult{
			ForLLM:  fmt.Sprintf("sending message: %v", err),
			IsError: true,
			Err:     err,
		}
	}

	t.sentInRound.Store(true)
	if flag := tools.RoundSentFlagFromCtx(ctx); flag != nil {
		flag.Store(true)
	}
	// Silent: user already received the message directly
	return &tools.ToolResult{
		ForLLM: fmt.Sprintf("Message sent to %s:%s", channel, chatID),
		Silent: true,
	}
}
