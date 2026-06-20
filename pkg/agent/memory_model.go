// ClawEh - Cognitive Memory
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	names     []string // model name per client (parallel to clients)
	modelName string   // first resolved model name, for run records
}

// ModelName returns the human-readable name of the first model in the chain.
func (c *memoryModelCaller) ModelName() string { return c.modelName }

// nameAt returns the model name for client index i, or the head model name as a
// fallback when names is short or empty.
func (c *memoryModelCaller) nameAt(i int) string {
	if i >= 0 && i < len(c.names) && c.names[i] != "" {
		return c.names[i]
	}
	return c.modelName
}

// Consolidate sends the system prompt and JSON user payload to the model chain
// and returns the first successful raw response. Builds the standard two-message
// (system, user) shape the worker expects.
func (c *memoryModelCaller) Consolidate(ctx context.Context, systemPrompt, userJSON string) (string, string, error) {
	if len(c.clients) == 0 {
		return "", "", errors.New("cogmem: no memory model configured")
	}
	msgs := []providers.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userJSON},
	}
	var lastErr error
	for i, client := range c.clients {
		name := c.nameAt(i)
		reply, err := client.Complete(ctx, msgs)
		if err != nil {
			lastErr = err
			logger.DebugCF("cogmem", "memory model call failed; trying next in chain", map[string]any{
				"model": name, "error": err.Error(),
			})
			continue
		}
		// An empty/whitespace reply (e.g. a reasoning-only response, or a model
		// that returned no content) cannot be parsed as the consolidation JSON.
		if strings.TrimSpace(reply.Content) == "" {
			lastErr = errors.New("model returned empty content")
			logger.DebugCF("cogmem", "memory model returned empty content; trying next in chain",
				map[string]any{"model": name})
			continue
		}
		// The reply must parse as a consolidation Output. A model that emits a
		// bare fence, prose, or truncated JSON is non-empty but unparseable —
		// fall through to the next model rather than handing the worker raw text
		// it would record as "invalid_json / unexpected end of JSON input".
		if _, perr := consolidate.ParseOutput(reply.Content); perr != nil {
			lastErr = errors.New("model output was not valid JSON")
			logger.DebugCF("cogmem", "memory model output was not valid JSON; trying next in chain",
				map[string]any{"model": name, "error": perr.Error()})
			continue
		}
		return reply.Content, name, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no usable response")
	}
	// Clean, human-readable error — never a raw JSON-parser message. The head
	// model name is returned so the run record still attributes the attempt.
	return "", c.modelName, fmt.Errorf("cogmem: memory models returned no usable response: %w", lastErr)
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
	var names []string
	modelName := ""
	for _, name := range resolveCompressModelChain(agentModels, globalModels) {
		if modelName == "" {
			modelName = name
		}
		clients = append(clients, al.buildCompressLLMClient(agent, name, "cogmem"))
		names = append(names, name)
	}
	// Agent's primary model as the final fallback, mirroring getContextManager.
	clients = append(clients, al.buildDefaultCompressLLMClient(agent, "cogmem"))
	names = append(names, agent.Model)
	if modelName == "" {
		modelName = agent.Model
	}

	return &memoryModelCaller{clients: clients, names: names, modelName: modelName}
}
