// ClawEh
// License: MIT

package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// capturingContextManager records the agent ID observed in ctx on the entry
// points that runAgentLoop and runLLMIteration use for compression. It lets a
// test assert that runAgentLoop attaches the agent ID to ctx before the first
// compression-capable call, which is the load-bearing detail for the missing
// agent_id= field in compression error logs.
type capturingContextManager struct {
	mu                         sync.Mutex
	addUserMsgAgentID          string
	buildAgentID               string
	preDispatchCheckAgentID    string
	checkAndCompressAgentID    string
	addAssistantMessageAgentID string
}

func (c *capturingContextManager) capture(field *string, ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if *field == "" {
		*field = providers.AgentIDFromContext(ctx)
	}
}

func (c *capturingContextManager) AddUserMessage(ctx context.Context, _ providers.Message) error {
	c.capture(&c.addUserMsgAgentID, ctx)
	return nil
}
func (c *capturingContextManager) AddAssistantMessage(ctx context.Context, _ providers.Message) error {
	c.capture(&c.addAssistantMessageAgentID, ctx)
	return nil
}
func (c *capturingContextManager) AddToolCallMessage(_ context.Context, _ providers.Message) error {
	return nil
}
func (c *capturingContextManager) AddToolResult(_ context.Context, _ providers.Message) error {
	return nil
}
func (c *capturingContextManager) RecordToolUse(_ ...string) {}
func (c *capturingContextManager) PreDispatchCheck(ctx context.Context, current []providers.Message) ([]providers.Message, error) {
	c.capture(&c.preDispatchCheckAgentID, ctx)
	return current, nil
}
func (c *capturingContextManager) CheckAndCompress(ctx context.Context, built []providers.Message) ([]providers.Message, error) {
	c.capture(&c.checkAndCompressAgentID, ctx)
	return built, nil
}
func (c *capturingContextManager) SetSystemPrompt(_ string)   {}
func (c *capturingContextManager) SetCallContext(_, _ string) {}
func (c *capturingContextManager) SetSessionToken(_ string)   {}
func (c *capturingContextManager) Build(ctx context.Context) ([]providers.Message, error) {
	c.capture(&c.buildAgentID, ctx)
	return []providers.Message{{Role: "user", Content: "hi"}}, nil
}
func (c *capturingContextManager) SweepEvictions(_ context.Context) []llmcontext.EvictionEvent {
	return nil
}
func (c *capturingContextManager) Compact(_ context.Context) error                    { return nil }
func (c *capturingContextManager) LastCompactionReport() *llmcontext.CompactionReport { return nil }
func (c *capturingContextManager) RenderedSummary() string                            { return "" }
func (c *capturingContextManager) ForceCompress(_ context.Context) error              { return nil }
func (c *capturingContextManager) Stats() llmcontext.ContextStats                     { return llmcontext.ContextStats{} }
func (c *capturingContextManager) Reset(_ context.Context) error                      { return nil }
func (c *capturingContextManager) Close(_ context.Context) error                      { return nil }

// finalLLMProvider is a provider that returns a normal terminal response so
// runLLMIteration exits its loop cleanly without invoking tools.
type finalLLMProvider struct {
	seenAgentID atomic.Value
}

func (p *finalLLMProvider) Chat(
	ctx context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	if id := providers.AgentIDFromContext(ctx); id != "" {
		p.seenAgentID.Store(id)
	}
	return &providers.LLMResponse{
		Content:   "ok",
		Normal:    true,
		ToolCalls: []providers.ToolCall{},
	}, nil
}

func (p *finalLLMProvider) GetDefaultModel() string { return "test-final" }

// TestRunAgentLoop_PropagatesAgentIDForCompression verifies that runAgentLoop
// attaches the agent ID to ctx before reaching any compression-capable entry
// point. PreDispatchCheck, CheckAndCompress, and AddUserMessage (which holds
// the in-loop triggerCheck path) must all see agent_id when the loop runs,
// otherwise compression error logs lose the agent attribution Eric saw.
func TestRunAgentLoop_PropagatesAgentIDForCompression(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Provider = &finalLLMProvider{}

	sessionKey := "agentid-propagation"

	// Inject a capturing context manager directly into the cache. The fast
	// path in getContextManager picks this up instead of constructing a real
	// llmcontext.ContextManager, so we can observe ctx at each entry point
	// without depending on real compression heuristics firing.
	stub := &capturingContextManager{}
	entry := &cmEntry{
		cm:           stub,
		sessionKey:   sessionKey,
		lastAccessed: time.Now(),
	}
	entry.refcount.Store(0)
	al.contextManagers.Store(agent.ID+":"+sessionKey, entry)

	opts := processOptions{
		SessionKey:   sessionKey,
		Channel:      "cli",
		ChatID:       "direct",
		UserMessage:  "hello",
		SendResponse: false,
	}

	if _, err := al.runAgentLoop(context.Background(), agent, opts); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	checks := []struct {
		name string
		got  string
	}{
		{"AddUserMessage (triggerCheck path)", stub.addUserMsgAgentID},
		{"Build", stub.buildAgentID},
		{"CheckAndCompress", stub.checkAndCompressAgentID},
		{"PreDispatchCheck", stub.preDispatchCheckAgentID},
	}
	for _, c := range checks {
		if c.got != agent.ID {
			t.Errorf("%s observed agent_id=%q, want %q", c.name, c.got, agent.ID)
		}
	}

	// The LLM call itself should also see the agent ID — this is the original
	// runLLMIteration wrap, kept for safety. It should agree with the
	// runAgentLoop-level wrap.
	if got, _ := agent.Provider.(*finalLLMProvider).seenAgentID.Load().(string); got != agent.ID {
		t.Errorf("Chat observed agent_id=%q, want %q", got, agent.ID)
	}
}
