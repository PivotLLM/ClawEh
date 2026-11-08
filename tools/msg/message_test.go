package msg

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/tools"
)

func TestMessageTool_Execute_Success(t *testing.T) {
	tool := NewMessageTool()

	var sentChannel, sentChatID, sentContent string
	tool.SetSendCallback(func(_ context.Context, channel, chatID, content string) error {
		sentChannel = channel
		sentChatID = chatID
		sentContent = content
		return nil
	})

	ctx := turnCtx("test-channel", "test-chat-id")
	args := map[string]any{
		"content": "Hello, world!",
	}

	result := tool.Execute(ctx, args)

	// Verify message was sent with correct parameters
	if sentChannel != "test-channel" {
		t.Errorf("Expected channel 'test-channel', got '%s'", sentChannel)
	}
	if sentChatID != "test-chat-id" {
		t.Errorf("Expected chatID 'test-chat-id', got '%s'", sentChatID)
	}
	if sentContent != "Hello, world!" {
		t.Errorf("Expected content 'Hello, world!', got '%s'", sentContent)
	}

	// Verify ToolResult meets US-011 criteria:
	// - Send success returns SilentResult (Silent=true)
	if !result.Silent {
		t.Error("Expected Silent=true for successful send")
	}

	// - ForLLM contains send status description
	if result.ForLLM != "Message delivered to test-channel:test-chat-id." {
		t.Errorf("Expected ForLLM 'Message delivered to test-channel:test-chat-id.', got '%s'", result.ForLLM)
	}

	// - ForUser is empty (user already received message directly)
	if result.ForUser != "" {
		t.Errorf("Expected ForUser to be empty, got '%s'", result.ForUser)
	}

	// - IsError should be false
	if result.IsError {
		t.Error("Expected IsError=false for successful send")
	}
}

// TestMessageTool_Execute_IgnoresSuppliedChannel is a security regression: the
// model must NOT be able to redirect a message to another channel/chat. Any
// channel/chat_id in args is ignored; the session's own source is always used.
func TestMessageTool_Execute_IgnoresSuppliedChannel(t *testing.T) {
	tool := NewMessageTool()

	var sentChannel, sentChatID string
	tool.SetSendCallback(func(_ context.Context, channel, chatID, content string) error {
		sentChannel = channel
		sentChatID = chatID
		return nil
	})

	ctx := turnCtx("session-channel", "session-chat-id")
	args := map[string]any{
		"content": "Test message",
		"channel": "other-channel", // must be ignored
		"chat_id": "other-chat-id", // must be ignored
	}

	result := tool.Execute(ctx, args)

	// The session's own channel/chatID must be used, never the supplied ones.
	if sentChannel != "session-channel" {
		t.Errorf("Expected channel 'session-channel' (supplied channel must be ignored), got '%s'", sentChannel)
	}
	if sentChatID != "session-chat-id" {
		t.Errorf("Expected chatID 'session-chat-id' (supplied chat_id must be ignored), got '%s'", sentChatID)
	}

	if !result.Silent {
		t.Error("Expected Silent=true")
	}
	if result.ForLLM != "Message delivered to session-channel:session-chat-id." {
		t.Errorf("Expected ForLLM 'Message delivered to session-channel:session-chat-id.', got '%s'", result.ForLLM)
	}
}

func TestMessageTool_Execute_SendFailure(t *testing.T) {
	tool := NewMessageTool()

	sendErr := errors.New("network error")
	tool.SetSendCallback(func(_ context.Context, channel, chatID, content string) error {
		return sendErr
	})

	ctx := turnCtx("test-channel", "test-chat-id")
	args := map[string]any{
		"content": "Test message",
	}

	result := tool.Execute(ctx, args)

	// Verify ToolResult for send failure:
	// - Send failure returns ErrorResult (IsError=true)
	if !result.IsError {
		t.Error("Expected IsError=true for failed send")
	}

	// - ForLLM contains error description
	expectedErrMsg := "Not sent: couldn't reach test-channel:test-chat-id."
	if result.ForLLM != expectedErrMsg {
		t.Errorf("Expected ForLLM '%s', got '%s'", expectedErrMsg, result.ForLLM)
	}

	// - Err field should contain original error
	if result.Err == nil {
		t.Error("Expected Err to be set")
	}
	if !errors.Is(result.Err, sendErr) {
		t.Errorf("Expected Err to be sendErr, got %v", result.Err)
	}
}

// TestMessageTool_Execute_Outcomes: the result says what became of the
// message, naming the chat once (no doubled channel prefix): delivered,
// queued when no outcome was known in time, or not sent and why. Only a
// delivered or queued message counts as the round's reply.
func TestMessageTool_Execute_Outcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chatID  string
		err     error
		want    string
		isError bool
		refusal bool
	}{
		{"delivered", "device:abc", nil, "Message delivered to device:abc.", false, false},
		{"delivered, bare chat id", "12345", nil, "Message delivered to device:12345.", false, false},
		{"queued", "device:abc", ErrQueued, "Message queued for delivery to device:abc.", false, false},
		{"offline", "device:abc", fmt.Errorf("%w: device not connected", channels.ErrRecipientOffline), "Not sent: device:abc is offline.", true, true},
		{"not found", "device:abc", fmt.Errorf("%w: no paired device", channels.ErrRecipientNotFound), "Not sent: device:abc can't be reached.", true, true},
		{"not set up", "device:abc", fmt.Errorf("%w: device", channels.ErrUnknownChannel), "Not sent: device:abc is not set up.", true, true},
		{"not running", "device:abc", fmt.Errorf("%w: device", channels.ErrNotRunning), "Not sent: device:abc is unavailable.", true, true},
		{"receive-only", "device:abc", channels.ErrReceiveOnly, "Not sent: device:abc is receive-only.", true, true},
		{"other failure", "device:abc", fmt.Errorf("%w: boom", channels.ErrSendFailed), "Not sent: couldn't reach device:abc.", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewMessageTool()
			tool.SetSendCallback(func(context.Context, string, string, string) error { return tc.err })
			var sent atomic.Bool
			ctx := tools.WithRoundSentFlag(turnCtx("device", tc.chatID), &sent)
			result := tool.Execute(ctx, map[string]any{"content": "hi"})
			if result.ForLLM != tc.want || result.IsError != tc.isError {
				t.Errorf("result = %q (error %v), want %q (error %v)", result.ForLLM, result.IsError, tc.want, tc.isError)
			}
			if tools.IsExpectedRefusal(result.Err) != tc.refusal {
				t.Errorf("expected refusal = %v, want %v", !tc.refusal, tc.refusal)
			}
			if sent.Load() == tc.isError || tool.HasSentInRound() == tc.isError {
				t.Errorf("round marked sent = %v, want %v", sent.Load(), !tc.isError)
			}
		})
	}
}

func TestMessageTool_Execute_MissingContent(t *testing.T) {
	tool := NewMessageTool()

	ctx := turnCtx("test-channel", "test-chat-id")
	args := map[string]any{} // content missing

	result := tool.Execute(ctx, args)

	// Verify error result for missing content
	if !result.IsError {
		t.Error("Expected IsError=true for missing content")
	}
	if result.ForLLM != "content is required" {
		t.Errorf("Expected ForLLM 'content is required', got '%s'", result.ForLLM)
	}
}

// An empty or whitespace-only content is refused before anything is sent:
// Slack rejects it (no_text) and nobody would receive it anyway.
func TestMessageTool_Execute_EmptyContent(t *testing.T) {
	for _, content := range []string{"", "   ", "\n\t"} {
		t.Run(fmt.Sprintf("%q", content), func(t *testing.T) {
			tool := NewMessageTool()
			sent := false
			tool.SetSendCallback(func(_ context.Context, _, _, _ string) error {
				sent = true
				return nil
			})

			ctx := turnCtx("test-channel", "test-chat-id")
			result := tool.Execute(ctx, map[string]any{"content": content})

			if sent {
				t.Error("Expected no send for empty content")
			}
			if !result.IsError {
				t.Error("Expected IsError=true for empty content")
			}
			if result.ForLLM != "content is empty: nothing to send" {
				t.Errorf("Expected ForLLM 'content is empty: nothing to send', got '%s'", result.ForLLM)
			}
		})
	}
}

// turnCtx is the context of a call whose turn replies to chatID on channel.
func turnCtx(channel, chatID string) context.Context {
	return tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: channel, ChatID: chatID})
}

// TestMessageTool_Destination: a message goes only to the chat of the call's
// turn; without one, when the call can't be tied to one turn, in a turn that
// answers an agent and on an internal channel, it is refused and nothing is
// sent. The tool context alone (the channel a tool runs for) is not a chat.
func TestMessageTool_Destination(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"no turn (service token)", context.Background(), "There is no chat to send messages to."},
		{"tool context only", tools.WithToolContext(context.Background(), "telegram", "42"), "There is no chat to send messages to."},
		{"more than one turn", tools.WithReplyChat(context.Background(), tools.ReplyChat{Ambiguous: true}), "Messages can't be sent now: this agent is answering more than one conversation."},
		{"asked turn", turnCtx(constants.AgentMessageChannel, "ask-1"), "Messages can't be sent to another agent: your final reply goes to it."},
		{"sub-agent turn", turnCtx(constants.SubagentChannel, "agent:alice:task"), "Messages can't be sent to another agent: your final reply goes to it."},
		{"claw agent (cli) turn", turnCtx("cli", "direct"), "This chat can't receive messages."},
		{"recovery turn", turnCtx("recovery", "x"), "This chat can't receive messages."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := false
			tool := NewMessageTool()
			tool.SetSendCallback(func(context.Context, string, string, string) error { sent = true; return nil })
			res := tool.Execute(tc.ctx, map[string]any{"content": "hi"})
			if !res.IsError || res.ForLLM != tc.want || sent {
				t.Fatalf("result = %+v sent=%v, want %q and nothing sent", res, sent, tc.want)
			}
			if !tools.IsExpectedRefusal(res.Err) {
				t.Fatalf("Err = %v, want an expected refusal (logged at WARN)", res.Err)
			}
		})
	}
}

func TestMessageTool_Execute_NotConfigured(t *testing.T) {
	tool := NewMessageTool()
	// No SetSendCallback called

	ctx := turnCtx("test-channel", "test-chat-id")
	args := map[string]any{
		"content": "Test message",
	}

	result := tool.Execute(ctx, args)

	// Verify error when send callback not configured
	if !result.IsError {
		t.Error("Expected IsError=true when send callback not configured")
	}
	if result.ForLLM != "Message sending not configured" {
		t.Errorf("Expected ForLLM 'Message sending not configured', got '%s'", result.ForLLM)
	}
}

func TestMessageTool_Name(t *testing.T) {
	tool := NewMessageTool()
	if tool.Name() != "msg_send" {
		t.Errorf("Expected name 'msg_send', got '%s'", tool.Name())
	}
}

func TestMessageTool_Description(t *testing.T) {
	tool := NewMessageTool()
	desc := tool.Description()
	if desc == "" {
		t.Error("Description should not be empty")
	}
}

func TestMessageTool_Parameters(t *testing.T) {
	tool := NewMessageTool()
	params := tool.Parameters()

	// Verify parameters structure
	typ, ok := params["type"].(string)
	if !ok || typ != "object" {
		t.Error("Expected type 'object'")
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("Expected properties to be a map")
	}

	// Check required properties
	required, ok := params["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "content" {
		t.Error("Expected 'content' to be required")
	}

	// Check content property
	contentProp, ok := props["content"].(map[string]any)
	if !ok {
		t.Error("Expected 'content' property")
	}
	if contentProp["type"] != "string" {
		t.Error("Expected content type to be 'string'")
	}

	// Security: channel/chat_id must NOT be model-selectable. The tool always
	// targets the session's own source channel.
	if _, present := props["channel"]; present {
		t.Error("'channel' must not be a model-facing parameter (target is locked to the session)")
	}
	if _, present := props["chat_id"]; present {
		t.Error("'chat_id' must not be a model-facing parameter (target is locked to the session)")
	}
}
