package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/agenttoken"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// capturingProvider records the messages it receives so tests can assert
// on the rendered system prompt.
type capturingProvider struct {
	lastMessages []providers.Message
}

func (p *capturingProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	p.lastMessages = append([]providers.Message{}, messages...)
	return &providers.LLMResponse{Content: "ok"}, nil
}

func (p *capturingProvider) GetDefaultModel() string { return "test-model" }
func (p *capturingProvider) SupportsTools() bool     { return false }
func (p *capturingProvider) GetContextWindow() int   { return 4096 }

// TestSubagentSystemPrompt_ContainsSentinel asserts that the rendered
// subagent system prompt includes the SubagentSentinel as defense-in-depth.
func TestSubagentSystemPrompt_ContainsSentinel(t *testing.T) {
	prompt := subagentSystemPrompt()
	if !strings.Contains(prompt, agenttoken.SubagentSentinel) {
		t.Errorf("subagent system prompt missing SubagentSentinel; got:\n%s", prompt)
	}
}

// TestSubagentTool_Execute_InjectsSubagentSentinel asserts the sentinel is
// present in the system message sent to the LLM by the synchronous tool path.
func TestSubagentTool_Execute_InjectsSubagentSentinel(t *testing.T) {
	provider := &capturingProvider{}
	manager := NewSubagentManager(SubagentManagerConfig{
		Provider:     provider,
		DefaultModel: "test-model",
		Workspace:    "/tmp/test",
	})
	tool := NewSubagentTool(manager)

	ctx := WithToolContext(context.Background(), "cli", "direct")
	res := tool.Execute(ctx, map[string]any{"task": "do something"})
	if res == nil || res.IsError {
		t.Fatalf("expected success, got: %+v", res)
	}

	if len(provider.lastMessages) == 0 {
		t.Fatal("provider received no messages")
	}
	sys := provider.lastMessages[0]
	if sys.Role != "system" {
		t.Fatalf("expected first message role 'system', got %q", sys.Role)
	}
	if !strings.Contains(sys.Content, agenttoken.SubagentSentinel) {
		t.Errorf("system prompt missing SubagentSentinel; got:\n%s", sys.Content)
	}
}
