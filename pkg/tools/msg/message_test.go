package msg

import (
	"context"

	"github.com/PivotLLM/ClawEh/pkg/tools"
	"errors"
	"testing"
)

func TestMessageTool_Execute_Success(t *testing.T) {
	tool := NewMessageTool()

	var sentChannel, sentChatID, sentContent string
	tool.SetSendCallback(func(channel, chatID, content string) error {
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
	if result.ForLLM != "Message sent to test-channel:test-chat-id" {
		t.Errorf("Expected ForLLM 'Message sent to test-channel:test-chat-id', got '%s'", result.ForLLM)
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
	tool.SetSendCallback(func(channel, chatID, content string) error {
		sentChannel = channel
		sentChatID = chatID
		return nil
	})

	ctx := tools.WithToolContext(context.Background(), "session-channel", "session-chat-id")
	args := map[string]any{
		"content": "Test message",
		"channel": "other-channel",  // must be ignored
		"chat_id": "other-chat-id",  // must be ignored
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
	if result.ForLLM != "Message sent to session-channel:session-chat-id" {
		t.Errorf("Expected ForLLM 'Message sent to session-channel:session-chat-id', got '%s'", result.ForLLM)
	}
}

func TestMessageTool_Execute_SendFailure(t *testing.T) {
	tool := NewMessageTool()

	sendErr := errors.New("network error")
	tool.SetSendCallback(func(channel, chatID, content string) error {
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
	expectedErrMsg := "sending message: network error"
	if result.ForLLM != expectedErrMsg {
		t.Errorf("Expected ForLLM '%s', got '%s'", expectedErrMsg, result.ForLLM)
	}

	// - Err field should contain original error
	if result.Err == nil {
		t.Error("Expected Err to be set")
	}
	if result.Err != sendErr {
		t.Errorf("Expected Err to be sendErr, got %v", result.Err)
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

	tool.SetSendCallback(func(channel, chatID, content string) error {
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
