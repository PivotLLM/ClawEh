// ClawEh - Cognitive Memory
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/PivotLLM/ClawEh/pkg/cogmem/consolidate"
	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// memoryModelCaller adapts the agent's summarization ("Memory") model chain to
// consolidate.ModelCaller. The cognitive-memory consolidation pass reuses the
// same model chain as context compaction — there is no separate memory-model
// config (see config.MemoryConfig). It tries each client in order and returns
// the first successful raw text; JSON-object response format is requested via
// the underlying providerLLMClient.
type memoryModelCaller struct {
	clients   []llmcontext.LLMClient
	modelName string // first resolved model name, for run records
}

// ModelName returns the human-readable name of the first model in the chain.
func (c *memoryModelCaller) ModelName() string { return c.modelName }

// Consolidate sends the system prompt and JSON user payload to the model chain
// and returns the first successful raw response. Builds the standard two-message
// (system, user) shape the worker expects.
func (c *memoryModelCaller) Consolidate(ctx context.Context, systemPrompt, userJSON string) (string, error) {
	if len(c.clients) == 0 {
		return "", errors.New("cogmem: no memory model configured")
	}
	msgs := []providers.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userJSON},
	}
	var lastErr error
	for _, client := range c.clients {
		reply, err := client.Complete(ctx, msgs)
		if err != nil {
			lastErr = err
			logger.DebugCF("cogmem", "memory model call failed; trying next in chain", map[string]any{
				"error": err.Error(),
			})
			continue
		}
		return reply.Content, nil
	}
	return "", fmt.Errorf("cogmem: all memory models failed: %w", lastErr)
}

// NewMemoryModelCaller builds a consolidate.ModelCaller for the given agent
// using the agent's summarization model chain (agent.summarization_models →
// global summarization.models → the agent's primary model as final fallback),
// resolved through the per-model dispatcher exactly like context compaction.
// This is the constructor the gateway's WorkerFactory calls per Job.
func (al *AgentLoop) NewMemoryModelCaller(agent *AgentInstance) consolidate.ModelCaller {
	var agentModels, globalModels []string
	if agent.Config != nil {
		agentModels = agent.Config.SummarizationModels
	}
	if cfg := al.GetConfig(); cfg != nil {
		globalModels = cfg.Summarization.Models
	}

	var clients []llmcontext.LLMClient
	modelName := ""
	for _, name := range resolveCompressModelChain(agentModels, globalModels) {
		if modelName == "" {
			modelName = name
		}
		clients = append(clients, al.buildCompressLLMClient(agent, name, "cogmem"))
	}
	// Agent's primary model as the final fallback, mirroring getContextManager.
	clients = append(clients, al.buildDefaultCompressLLMClient(agent, "cogmem"))
	if modelName == "" {
		modelName = agent.Model
	}

	return &memoryModelCaller{clients: clients, modelName: modelName}
}
