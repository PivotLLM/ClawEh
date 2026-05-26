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
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/providers/openai_compat"
)

// providerLLMClient adapts providers.LLMProvider to llmcontext.LLMClient so
// the ContextManager can use the agent's configured provider for compression
// LLM calls without depending on the concrete provider type.
//
// requestJSONObject, when true, asks the underlying provider to honour
// response_format={"type":"json_object"} on the outbound request. The
// provider gates emission on protocol capability (see openai_compat.Provider);
// non-capable providers silently drop the request and log DBG.
type providerLLMClient struct {
	provider          providers.LLMProvider
	model             string
	requestJSONObject bool
}

func (c *providerLLMClient) Complete(ctx context.Context, messages []providers.Message) (providers.Message, error) {
	var opts map[string]any
	if c.requestJSONObject {
		opts = map[string]any{
			openai_compat.ResponseFormatJSONObjectOption: true,
		}
	}
	resp, err := c.provider.Chat(ctx, messages, nil, c.model, opts)
	if err != nil {
		return providers.Message{}, err
	}
	return providers.Message{Role: "assistant", Content: resp.Content}, nil
}

// resolveCompressModelTarget resolves a configured compress_model reference into
// the (alias, protocol, modelID) triple that can be handed to the provider
// dispatcher. Bare aliases and shorthand model IDs are looked up against the
// loaded model_list, mirroring resolveFromModelList in instance.go.
//
// The returned alias is the resolved entry's model_name; the dispatcher uses
// it as the cache/lookup key so per-entry openai_compat state
// (response_log_file, reasoning_effort, extra_body, …) is honoured when
// multiple entries share the same wire model.
//
// Returns ("", "", "", false) when the reference cannot be resolved against the
// configured model_list — callers should then fall back to the agent's default
// provider rather than guess a protocol.
func resolveCompressModelTarget(cfg *config.Config, raw string) (alias, protocol, modelID string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", false
	}
	if cfg == nil {
		return "", "", "", false
	}

	// Direct alias / ModelName lookup wins: this lets users say
	// compress_model: "haiku" and have it resolve to the model_list entry
	// whose model_name == "haiku".
	if mc, err := cfg.GetModelConfig(raw); err == nil && mc != nil {
		full := strings.TrimSpace(mc.Model)
		if full != "" {
			p, m := providers.ExtractProtocol(full)
			return mc.ModelName, p, m, true
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
			return cfg.ModelList[i].ModelName, p, m, true
		}
		p, m := providers.ExtractProtocol(full)
		if m == raw {
			return cfg.ModelList[i].ModelName, p, m, true
		}
	}

	// If raw already carries a protocol prefix, use it verbatim. Dispatcher
	// will still need a matching enabled model_list entry, but accepting the
	// prefix here makes the failure message in the dispatcher useful. No
	// alias is known in this branch, so dispatcher falls back to wire-model
	// matching.
	if strings.Contains(raw, "/") {
		p, m := providers.ExtractProtocol(raw)
		return "", p, m, true
	}

	return "", "", "", false
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
	alias, protocol, modelID, ok := resolveCompressModelTarget(cfg, compressModelName)
	if ok && al.dispatcher != nil {
		// Prefer the resolved alias as the dispatcher key so per-entry
		// openai_compat state is honoured. Fall back to the wire model when
		// no alias is known (raw protocol/modelID inputs).
		key := alias
		if key == "" {
			key = protocol + "/" + modelID
		}
		if p, err := al.dispatcher.Get(key); err == nil {
			log.Printf("agent: compress_model %q dispatched via alias=%q (protocol=%q model=%q) for agent %q session %q",
				compressModelName, alias, protocol, modelID, agent.ID, sessionKey)
			return &providerLLMClient{provider: p, model: modelID, requestJSONObject: true}
		} else {
			log.Printf("agent: dispatcher could not build compress provider for %q (alias=%q %q/%q): %v; falling back to agent.Provider",
				compressModelName, alias, protocol, modelID, err)
		}
	} else if !ok {
		log.Printf("agent: compress_model %q did not match any enabled model_list entry; falling back to agent.Provider",
			compressModelName)
	}
	// Last-resort fallback: use the agent's primary provider with the raw
	// compress_model string so the existing single-provider configurations
	// (e.g. all-anthropic, all-openai deployments) keep working without a
	// model_list entry.
	return &providerLLMClient{provider: agent.Provider, model: compressModelName, requestJSONObject: true}
}

// buildDefaultCompressLLMClient returns the compression LLM client used when
// compress_model is not configured. It defaults to the agent's primary model
// and resolves it through the per-model dispatcher, mirroring the explicit
// compress_model path. This prevents compression from silently routing
// through the shared agent.Provider — which on a "claude-cli" default would
// shell out to claude-cli for every compression call regardless of the
// agent's actual primary protocol (e.g. an agent with model.primary =
// "openai/grok-4.3" would otherwise see compression dispatched to
// claude-cli and 404 because grok-4.3 isn't a Claude model). Falls back to
// agent.Provider only when the dispatcher cannot satisfy the agent's
// primary model.
func (al *AgentLoop) buildDefaultCompressLLMClient(agent *AgentInstance, sessionKey string) llmcontext.LLMClient {
	cfg := al.GetConfig()
	primary := strings.TrimSpace(agent.Model)
	if primary != "" && al.dispatcher != nil {
		if alias, protocol, modelID, ok := resolveCompressModelTarget(cfg, primary); ok {
			key := alias
			if key == "" {
				key = protocol + "/" + modelID
			}
			if p, err := al.dispatcher.Get(key); err == nil {
				logger.DebugCF("llmcontext", "compress_model unset; defaulting to agent primary via dispatcher", map[string]any{
					"agent_id": agent.ID,
					"alias":    alias,
					"model":    modelID,
					"protocol": protocol,
					"session":  sessionKey,
				})
				return &providerLLMClient{provider: p, model: modelID, requestJSONObject: true}
			} else {
				log.Printf("agent: dispatcher could not build default compress provider for primary %q (alias=%q %q/%q): %v; falling back to agent.Provider",
					primary, alias, protocol, modelID, err)
			}
		}
	}
	// Last-resort fallback: use the agent's primary provider directly.
	return &providerLLMClient{provider: agent.Provider, model: primary, requestJSONObject: true}
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
		// No compress_model configured: default to the agent's primary model
		// resolved through the dispatcher, so compression runs against the
		// agent's actual primary protocol instead of the shared agent.Provider
		// (which on a "claude-cli" default would mis-route compression for
		// any non-Claude primary). See buildDefaultCompressLLMClient.
		compressClients = []llmcontext.LLMClient{al.buildDefaultCompressLLMClient(agent, sessionKey)}
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
