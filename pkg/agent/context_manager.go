// ClawEh
// License: MIT

package agent

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
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

// resolveCompressModelTarget resolves a configured compress_model reference into
// the fully-qualified (protocol, modelID) pair that can be handed to the
// provider dispatcher. Bare aliases and shorthand model IDs are looked up
// against the loaded model_list, mirroring resolveFromModelList in instance.go.
// Returns ("", "", false) when the reference cannot be resolved against the
// configured model_list — callers should then fall back to the agent's default
// provider rather than guess a protocol.
func resolveCompressModelTarget(cfg *config.Config, raw string) (protocol, modelID string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	if cfg == nil {
		return "", "", false
	}

	// Direct alias / ModelName lookup wins: this lets users say
	// compress_model: "haiku" and have it resolve to the model_list entry
	// whose model_name == "haiku".
	if mc, err := cfg.GetModelConfig(raw); err == nil && mc != nil {
		full := strings.TrimSpace(mc.Model)
		if full != "" {
			p, m := providers.ExtractProtocol(full)
			return p, m, true
		}
	}

	// Otherwise scan enabled model_list entries: match by the full
	// "protocol/modelID" string or by the bare modelID component.
	for i := range cfg.ModelList {
		if !cfg.ModelList[i].Enabled {
			continue
		}
		full := strings.TrimSpace(cfg.ModelList[i].Model)
		if full == "" {
			continue
		}
		if full == raw {
			p, m := providers.ExtractProtocol(full)
			return p, m, true
		}
		p, m := providers.ExtractProtocol(full)
		if m == raw {
			return p, m, true
		}
	}

	// If raw already carries a protocol prefix, use it verbatim. Dispatcher
	// will still need a matching enabled model_list entry, but accepting the
	// prefix here makes the failure message in the dispatcher useful.
	if strings.Contains(raw, "/") {
		p, m := providers.ExtractProtocol(raw)
		return p, m, true
	}

	return "", "", false
}

// buildCompressLLMClient returns the LLMClient used to drive context-window
// compression for the given agent. When the configured compress_model resolves
// against the loaded model_list, the per-protocol provider is constructed via
// the dispatcher so non-default protocols (anthropic, openai, openrouter, xai…)
// don't get accidentally routed through the shared agent.Provider — which on a
// "claude-cli" default tries to shell out to claude-cli for every compression
// pass regardless of the compress_model setting. Falls back to agent.Provider
// only when the dispatcher cannot satisfy the request.
func (al *AgentLoop) buildCompressLLMClient(agent *AgentInstance, compressModelName, sessionKey string) llmcontext.LLMClient {
	cfg := al.GetConfig()
	protocol, modelID, ok := resolveCompressModelTarget(cfg, compressModelName)
	if ok && al.dispatcher != nil {
		if p, err := al.dispatcher.Get(protocol, modelID); err == nil {
			log.Printf("agent: compress_model %q dispatched to protocol=%q model=%q for agent %q session %q",
				compressModelName, protocol, modelID, agent.ID, sessionKey)
			return &providerLLMClient{provider: p, model: modelID}
		} else {
			log.Printf("agent: dispatcher could not build compress provider for %q (%q/%q): %v; falling back to agent.Provider",
				compressModelName, protocol, modelID, err)
		}
	} else if !ok {
		log.Printf("agent: compress_model %q did not match any enabled model_list entry; falling back to agent.Provider",
			compressModelName)
	}
	// Last-resort fallback: use the agent's primary provider with the raw
	// compress_model string so the existing single-provider configurations
	// (e.g. all-anthropic, all-openai deployments) keep working without a
	// model_list entry.
	return &providerLLMClient{provider: agent.Provider, model: compressModelName}
}

// getContextManager returns the ContextManager for the given agent+session pair,
// creating and caching it on first access. The returned manager is shared across
// all calls for the same (agentID, sessionKey) tuple.
//
// The returned release function must be deferred by the caller to decrement the
// reference count. The eviction goroutine skips entries with refcount > 0.
func (al *AgentLoop) getContextManager(agent *AgentInstance, sessionKey string) (llmcontext.ContextManager, func()) {
	key := agent.ID + ":" + sessionKey

	// Fast path: entry already exists.
	if v, ok := al.contextManagers.Load(key); ok {
		entry := v.(*cmEntry)
		entry.refcount.Add(1)
		entry.lastAccessed = time.Now()
		release := func() { entry.refcount.Add(-1) }
		return entry.cm, release
	}

	// Slow path: create a new ContextManager and wrap it in a cmEntry.

	// Primary LLM client used for normal dispatch and as fallback for compression.
	llmClient := &providerLLMClient{provider: agent.Provider, model: agent.Model}

	// Resolve compress_model: agent-level config takes precedence over defaults.
	// Both the agent config and the defaults may specify a compress_model.
	var compressClients []llmcontext.LLMClient
	compressModelName := ""

	// Check agent-level config first, then agent defaults via the loaded config.
	if agent.Config != nil && agent.Config.CompressModel != nil {
		compressModelName = strings.TrimSpace(agent.Config.CompressModel.Primary)
	}
	if compressModelName == "" {
		// Fall back to agent defaults from the loaded config.
		if cfg := al.GetConfig(); cfg != nil {
			dm := strings.TrimSpace(cfg.Agents.Defaults.CompressModel.Primary)
			if dm != "" {
				compressModelName = dm
			}
		}
	}

	if compressModelName != "" {
		// Build the compress client through the per-model dispatcher so the
		// configured compress_model is invoked through its own protocol's
		// provider, not the agent's default provider. See buildCompressLLMClient
		// for the resolution rules and fallback behaviour.
		compressClient := al.buildCompressLLMClient(agent, compressModelName, sessionKey)
		// compress client is tried first; primary model is the fallback.
		compressClients = []llmcontext.LLMClient{compressClient, llmClient}
	} else {
		// No compress_model configured: use the primary client only.
		compressClients = []llmcontext.LLMClient{llmClient}
	}

	// The archive directory is the sessions directory within the agent workspace.
	// We derive it from the workspace the same way initSessionStore does.
	archiveDir := filepath.Join(agent.Workspace, "sessions")
	opts := append([]llmcontext.Option{
		llmcontext.WithContextWindow(agent.ContextWindow),
		llmcontext.WithArchiveDir(archiveDir),
		llmcontext.WithCompressLLM(compressClients...),
	}, agent.CompressOpts...)
	cm := llmcontext.New(sessionKey, agent.Sessions, agent.ContextBuilder, llmClient, opts...)

	// Issue a session token so session-scoped MCP tools can identify this session.
	// The token is injected into the system prompt via cm.SetSessionToken so the
	// LLM receives it on every Build() call.
	al.mu.RLock()
	sti := al.sessionTokenIssuer
	al.mu.RUnlock()
	if sti != nil {
		tok := sti.Issue(agent.ID, sessionKey, archiveDir)
		if tok != "" {
			cm.SetSessionToken(tok)
		}
	}

	newEntry := &cmEntry{
		cm:           cm,
		sessionKey:   sessionKey,
		lastAccessed: time.Now(),
	}
	newEntry.refcount.Store(1)

	actual, loaded := al.contextManagers.LoadOrStore(key, newEntry)
	if loaded {
		// Another goroutine beat us; use theirs and discard ours.
		// The one we created (cm) is not stored and will be GC'd.
		// Revoke the token we just issued since we won't use this CM.
		if sti != nil {
			sti.Revoke(sessionKey)
		}
		entry := actual.(*cmEntry)
		entry.refcount.Add(1)
		entry.lastAccessed = time.Now()
		release := func() { entry.refcount.Add(-1) }
		return entry.cm, release
	}

	release := func() { newEntry.refcount.Add(-1) }
	return newEntry.cm, release
}
