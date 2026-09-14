// ClawEh - Cognitive Memory
// License: MIT

package agent

import (
	"context"

	"github.com/PivotLLM/cogmem/consolidate"
)

// chainCompleter is the plain-typed chain walk the host's summarization
// caller exposes (compressModelCaller.complete), so the same walk can serve a
// second caller shape without either module importing the other.
type chainCompleter func(ctx context.Context, system, user string, jsonObject bool, exclude []string) (content, finishReason, model string, err error)

// memoryModelCaller adapts the agent's summarization model chain to
// consolidate.ModelCaller. Consolidation reuses the same chain as context
// compaction — there is no separate memory-model config (see
// config.MemoryConfig) — and the same host client: one chain walk, honouring
// Exclude and the shared cooldown tracker, serving both modules. cogmem
// validates the reply itself and calls again with a longer Exclude when a
// model returns something unusable.
type memoryModelCaller struct {
	complete  chainCompleter
	modelName string // first resolved model name, for run records
}

// ModelName returns the human-readable name of the first model in the chain.
func (c *memoryModelCaller) ModelName() string { return c.modelName }

// Complete implements consolidate.ModelCaller.
func (c *memoryModelCaller) Complete(ctx context.Context, req consolidate.ModelRequest) (consolidate.ModelReply, error) {
	content, finishReason, model, err := c.complete(ctx, req.System, req.User, req.JSONObject, req.Exclude)
	return consolidate.ModelReply{Content: content, FinishReason: finishReason, Model: model}, err
}

// NewMemoryModelCaller builds a consolidate.ModelCaller for the given agent
// over the agent's summarization model chain (agent.summarization_models →
// global summarization.models → the agent's primary model as final fallback),
// resolved through the per-model dispatcher exactly like context compaction.
// This is the constructor the gateway's WorkerFactory calls per Job.
func (al *AgentLoop) NewMemoryModelCaller(agent *AgentInstance) consolidate.ModelCaller {
	caller, effective := al.newCompressModelCaller(agent, "cogmem")
	return &memoryModelCaller{complete: caller.complete, modelName: effective}
}
