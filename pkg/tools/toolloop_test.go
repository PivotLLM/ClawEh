package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// mockLLMProvider is a simple mock for testing the tool loop.
type mockLLMProvider struct {
	responses []*providers.LLMResponse
	errs      []error
	callCount int
}

func (m *mockLLMProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	idx := m.callCount
	m.callCount++
	if idx < len(m.errs) && m.errs[idx] != nil {
		return nil, m.errs[idx]
	}
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	return &providers.LLMResponse{Content: "default", FinishReason: "stop"}, nil
}

func (m *mockLLMProvider) GetDefaultModel() string { return "mock-model" }

// mockTool is a simple tool for testing.
type mockTool struct {
	name   string
	result *ToolResult
}

func (t *mockTool) Name() string              { return t.name }
func (t *mockTool) Description() string       { return "mock tool " + t.name }
func (t *mockTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *mockTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t.result != nil {
		return t.result
	}
	return NewToolResult("tool executed: " + t.name)
}

func TestRunToolLoop_DirectAnswer(t *testing.T) {
	provider := &mockLLMProvider{
		responses: []*providers.LLMResponse{
			{Content: "The answer is 42", FinishReason: "stop"},
		},
	}

	cfg := ToolLoopConfig{
		Provider:      provider,
		Model:         "mock-model",
		MaxIterations: 5,
	}

	messages := []providers.Message{
		{Role: "user", Content: "What is the answer?"},
	}

	result, err := RunToolLoop(context.Background(), cfg, messages, "test-channel", "test-chat")
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if result.Content != "The answer is 42" {
		t.Errorf("Content = %q, want 'The answer is 42'", result.Content)
	}
	if result.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", result.Iterations)
	}
}

func TestRunToolLoop_LLMError(t *testing.T) {
	provider := &mockLLMProvider{
		errs: []error{errors.New("connection failed")},
	}

	cfg := ToolLoopConfig{
		Provider:      provider,
		Model:         "mock-model",
		MaxIterations: 5,
	}

	messages := []providers.Message{
		{Role: "user", Content: "Hello"},
	}

	_, err := RunToolLoop(context.Background(), cfg, messages, "", "")
	if err == nil {
		t.Fatal("expected error when LLM fails")
	}
	if !errors.Is(err, err) || err.Error() == "" {
		t.Error("expected non-empty error")
	}
}

func TestRunToolLoop_WithToolCall(t *testing.T) {
	// First response: LLM calls a tool.
	// Second response: LLM gives a final answer after receiving tool result.
	provider := &mockLLMProvider{
		responses: []*providers.LLMResponse{
			{
				Content:      "Let me check the weather.",
				FinishReason: "tool_calls",
				ToolCalls: []providers.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Name: "get_weather",
						Function: &providers.FunctionCall{
							Name:      "get_weather",
							Arguments: `{"location":"NYC"}`,
						},
					},
				},
			},
			{
				Content:      "The weather in NYC is sunny.",
				FinishReason: "stop",
			},
		},
	}

	registry := NewToolRegistry()
	registry.Register(&mockTool{
		name:   "get_weather",
		result: NewToolResult("Sunny, 72°F"),
	})

	cfg := ToolLoopConfig{
		Provider:      provider,
		Model:         "mock-model",
		Tools:         registry,
		MaxIterations: 10,
	}

	messages := []providers.Message{
		{Role: "user", Content: "What's the weather in NYC?"},
	}

	result, err := RunToolLoop(context.Background(), cfg, messages, "cli", "direct")
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if result.Content != "The weather in NYC is sunny." {
		t.Errorf("Content = %q, want 'The weather in NYC is sunny.'", result.Content)
	}
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", result.Iterations)
	}
	if provider.callCount != 2 {
		t.Errorf("LLM called %d times, want 2", provider.callCount)
	}
}

func TestRunToolLoop_MaxIterationsReached(t *testing.T) {
	// LLM always requests a tool call — should stop at max iterations.
	provider := &mockLLMProvider{}
	for range 10 {
		provider.responses = append(provider.responses, &providers.LLMResponse{
			Content:      "Checking...",
			FinishReason: "tool_calls",
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Name: "check",
					Function: &providers.FunctionCall{
						Name:      "check",
						Arguments: `{}`,
					},
				},
			},
		})
	}

	registry := NewToolRegistry()
	registry.Register(&mockTool{name: "check"})

	cfg := ToolLoopConfig{
		Provider:      provider,
		Model:         "mock-model",
		Tools:         registry,
		MaxIterations: 3,
	}

	messages := []providers.Message{
		{Role: "user", Content: "Do the thing"},
	}

	result, err := RunToolLoop(context.Background(), cfg, messages, "", "")
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	// After max iterations with no final answer, content is empty (loop exhausted)
	if result.Iterations != 3 {
		t.Errorf("Iterations = %d, want 3", result.Iterations)
	}
}

func TestRunToolLoop_NilTools(t *testing.T) {
	// Tool calls with nil registry — should use ErrorResult fallback.
	provider := &mockLLMProvider{
		responses: []*providers.LLMResponse{
			{
				Content:      "Calling tool...",
				FinishReason: "tool_calls",
				ToolCalls: []providers.ToolCall{
					{
						ID:   "call_x",
						Type: "function",
						Name: "missing_tool",
						Function: &providers.FunctionCall{
							Name:      "missing_tool",
							Arguments: `{}`,
						},
					},
				},
			},
			{
				Content:      "I couldn't do that.",
				FinishReason: "stop",
			},
		},
	}

	cfg := ToolLoopConfig{
		Provider:      provider,
		Model:         "mock-model",
		Tools:         nil, // nil tools
		MaxIterations: 5,
	}

	messages := []providers.Message{
		{Role: "user", Content: "Try the tool"},
	}

	result, err := RunToolLoop(context.Background(), cfg, messages, "", "")
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	// Should still return some result (second LLM call)
	if result.Iterations < 1 {
		t.Error("expected at least 1 iteration")
	}
}

func TestRunToolLoop_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	provider := &mockLLMProvider{
		errs: []error{context.Canceled},
	}

	cfg := ToolLoopConfig{
		Provider:      provider,
		Model:         "mock-model",
		MaxIterations: 5,
	}

	messages := []providers.Message{
		{Role: "user", Content: "Hello"},
	}

	_, err := RunToolLoop(ctx, cfg, messages, "", "")
	if err == nil {
		t.Fatal("expected error when context is canceled")
	}
}
