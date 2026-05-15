// ClawEh
// License: MIT

package agent

import (
	"context"

	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// providerLLMClient adapts providers.LLMProvider to llmcontext.LLMClient so
// the ContextManager can use the agent's configured provider for compression
// LLM calls without depending on the concrete provider type.
type providerLLMClient struct {
	provider providers.LLMProvider
	model    string
}

func (c *providerLLMClient) Complete(ctx context.Context, messages []providers.Message) (providers.Message, error) {
	resp, err := c.provider.Chat(ctx, messages, nil, c.model, nil)
	if err != nil {
		return providers.Message{}, err
	}
	return providers.Message{Role: "assistant", Content: resp.Content}, nil
}

// getContextManager returns the ContextManager for the given agent+session pair,
// creating and caching it on first access. The returned manager is shared across
// all calls for the same (agentID, sessionKey) tuple.
func (al *AgentLoop) getContextManager(agent *AgentInstance, sessionKey string) llmcontext.ContextManager {
	key := agent.ID + ":" + sessionKey
	if v, ok := al.contextManagers.Load(key); ok {
		return v.(llmcontext.ContextManager)
	}
	llmClient := &providerLLMClient{provider: agent.Provider, model: agent.Model}
	opts := append([]llmcontext.Option{llmcontext.WithContextWindow(agent.ContextWindow)}, agent.CompressOpts...)
	cm := llmcontext.New(sessionKey, agent.Sessions, agent.ContextBuilder, llmClient, opts...)
	actual, _ := al.contextManagers.LoadOrStore(key, cm)
	return actual.(llmcontext.ContextManager)
}
