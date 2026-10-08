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

	ctx := tools.WithToolContext(context.Background(), "test-channel", "test-chat-id")
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

	ctx := tools.WithToolContext(context.Background(), "session-channel", "session-chat-id")
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

	ctx := tools.WithToolContext(context.Background(), "test-channel", "test-chat-id")
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
			ctx := tools.WithRoundSentFlag(tools.WithToolContext(context.Background(), "device", tc.chatID), &sent)
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

	ctx := tools.WithToolContext(context.Background(), "test-channel", "test-chat-id")
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

func TestMessageTool_Execute_NoTargetChannel(t *testing.T) {
	tool := NewMessageTool()
	// No WithToolContext — channel/chatID are empty

	tool.SetSendCallback(func(_ context.Context, channel, chatID, content string) error {
		return nil
	})

	ctx := context.Background()
	args := map[string]any{
		"content": "Test message",
	}

	result := tool.Execute(ctx, args)

	// Verify error when no target channel specified
	if !result.IsError {
		t.Error("Expected IsError=true when no target channel")
	}
	if result.ForLLM != "No target channel/chat specified" {
		t.Errorf("Expected ForLLM 'No target channel/chat specified', got '%s'", result.ForLLM)
	}
}

func TestMessageTool_Execute_NotConfigured(t *testing.T) {
	tool := NewMessageTool()
	// No SetSendCallback called

	ctx := tools.WithToolContext(context.Background(), "test-channel", "test-chat-id")
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

// TestMessageTool_RefusedInAskedTurn: in an asked turn msg_send has no chat to
// send to and says so instead of reporting success.
func TestMessageTool_RefusedInAskedTurn(t *testing.T) {
	sent := false
	tool := NewMessageTool()
	tool.SetSendCallback(func(context.Context, string, string, string) error { sent = true; return nil })
	ctx := tools.WithToolContext(context.Background(), constants.AgentMessageChannel, "ask-1")
	res := tool.Execute(ctx, map[string]any{"content": "hi"})
	if !res.IsError || res.ForLLM != "msg_send needs a target in an asked turn" || sent {
		t.Fatalf("result = %+v sent=%v, want the refusal and nothing sent", res, sent)
	}
	ctx = tools.WithToolContext(context.Background(), "telegram", "chat-1")
	if res := tool.Execute(ctx, map[string]any{"content": "hi"}); res.IsError || !sent {
		t.Fatalf("result on a chat = %+v sent=%v, want it sent", res, sent)
	}
}
