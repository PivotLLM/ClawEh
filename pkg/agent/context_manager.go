// ClawEh
// License: MIT

package agent

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/constants"
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
	providerName      string // provider NAME (config), for the shared cooldown key
	requestJSONObject bool
}

// Model returns the model name this client dispatches to, used to label
// per-invocation entries in the compaction report.
func (c *providerLLMClient) Model() string { return c.model }

// CooldownProvider returns the provider name so the compaction path keys
// cooldown by provider+model identically to the main fallback chain (shared
// tracker). Empty when unknown (last-resort fallback client).
func (c *providerLLMClient) CooldownProvider() string { return c.providerName }

func (c *providerLLMClient) Complete(ctx context.Context, messages []providers.Message) (llmcontext.LLMReply, error) {
	var opts map[string]any
	if c.requestJSONObject {
		opts = map[string]any{
			openai_compat.ResponseFormatJSONObjectOption: true,
		}
	}
	resp, err := c.provider.Chat(ctx, messages, nil, c.model, opts)
	if err != nil {
		return llmcontext.LLMReply{}, err
	}
	return llmcontext.LLMReply{Content: resp.Content, FinishReason: resp.FinishReason}, nil
}

// resolveCompressModelTarget resolves a configured compress_model reference into
// the (alias, protocol, modelID) triple that can be handed to the provider
// dispatcher. Bare aliases and shorthand model IDs are looked up against the
// loaded models, mirroring resolveFromModelList in instance.go.
//
// The returned alias is the resolved entry's model_name; the dispatcher uses
// it as the cache/lookup key so per-entry openai_compat state
// (response_log_file, reasoning_effort, extra_body, …) is honoured when
// multiple entries share the same wire model.
//
// Returns ("", "", "", false) when the reference cannot be resolved against the
// configured models — callers should then fall back to the agent's default
// provider rather than guess a protocol.
// resolveCompressModelChain returns the ordered, de-duplicated summarization
// model chain for an agent: its own summarization_models first, then the global
// summarization.models. Blank entries are skipped; the first occurrence of each
// model name wins. The agent's primary model is appended separately by the
// caller as a final fallback.
func resolveCompressModelChain(agentModels, globalModels []string) []string {
	seen := make(map[string]struct{}, len(agentModels)+len(globalModels))
	var out []string
	for _, list := range [][]string{agentModels, globalModels} {
		for _, raw := range list {
			name := strings.TrimSpace(raw)
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

// compressProviderName resolves the provider NAME for a model alias, so the
// compaction client keys cooldown by the same provider+model as the main chain.
func compressProviderName(cfg *config.Config, alias string) string {
	if cfg == nil {
		return ""
	}
	if mc, err := cfg.GetModelConfig(alias); err == nil && mc != nil {
		return mc.Provider
	}
	return ""
}

func resolveCompressModelTarget(cfg *config.Config, raw string) (alias, modelID string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || cfg == nil {
		return "", "", false
	}

	// Direct alias / ModelName lookup wins: this lets users say
	// compress_model: "haiku" and have it resolve to the models entry
	// whose model_name == "haiku".
	if mc, err := cfg.GetModelConfig(raw); err == nil && mc != nil && mc.Model != "" {
		return mc.ModelName, mc.Model, true
	}

	// Otherwise scan enabled models entries: match by the raw model id.
	for i := range cfg.Models {
		if !cfg.Models[i].Enabled {
			continue
		}
		if strings.TrimSpace(cfg.Models[i].Model) == raw {
			return cfg.Models[i].ModelName, cfg.Models[i].Model, true
		}
	}

	return "", "", false
}

// buildCompressLLMClient returns the LLMClient used to drive context-window
// compression for the given agent. When the configured compress_model resolves
// against the loaded models, the per-protocol provider is constructed via
// the dispatcher so non-default protocols (anthropic, openai, openrouter, xai…)
// don't get accidentally routed through the shared agent.Provider — which on a
// "claude-cli" default tries to shell out to claude-cli for every compression
// pass regardless of the compress_model setting. Falls back to agent.Provider
// only when the dispatcher cannot satisfy the request.
func (al *AgentLoop) buildCompressLLMClient(agent *AgentInstance, compressModelName, sessionKey string) llmcontext.LLMClient {
	cfg := al.GetConfig()
	alias, modelID, ok := resolveCompressModelTarget(cfg, compressModelName)
	if ok && al.dispatcher != nil {
		// The dispatcher keys on the model_name alias and resolves the provider
		// from the model's provider reference.
		if p, err := al.dispatcher.Get(alias); err == nil {
			logger.DebugCF("llmcontext", "compression model resolved", map[string]any{
				"agent_id":  agent.ID,
				"requested": compressModelName,
				"alias":     alias,
				"model":     modelID,
				"session":   sessionKey,
			})
			return &providerLLMClient{provider: p, model: modelID, providerName: compressProviderName(cfg, alias), requestJSONObject: true}
		} else {
			logger.WarnCF("llmcontext", "compression model dispatch failed; falling back to agent provider", map[string]any{
				"agent_id":  agent.ID,
				"requested": compressModelName,
				"alias":     alias,
				"model":     modelID,
				"session":   sessionKey,
				"error":     err.Error(),
			})
		}
	} else if !ok {
		logger.WarnCF("llmcontext", "compression model not found in enabled models; falling back to agent provider", map[string]any{
			"agent_id":  agent.ID,
			"requested": compressModelName,
			"session":   sessionKey,
		})
	}
	// Last-resort fallback: use the agent's primary provider with the raw
	// compress_model string so the existing single-provider configurations
	// (e.g. all-anthropic, all-openai deployments) keep working without a
	// models entry.
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
		if alias, modelID, ok := resolveCompressModelTarget(cfg, primary); ok {
			if p, err := al.dispatcher.Get(alias); err == nil {
				logger.DebugCF("llmcontext", "compression: agent primary appended as final fallback", map[string]any{
					"agent_id": agent.ID,
					"alias":    alias,
					"model":    modelID,
					"session":  sessionKey,
				})
				return &providerLLMClient{provider: p, model: modelID, requestJSONObject: true}
			} else {
				logger.WarnCF("llmcontext", "compression: default (agent primary) dispatch failed; using agent provider directly", map[string]any{
					"agent_id": agent.ID,
					"alias":    alias,
					"model":    modelID,
					"session":  sessionKey,
					"error":    err.Error(),
				})
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

	// Resolve the summarization model chain. Order: the agent's own
	// summarization_models (optional) first, then the global
	// cfg.Summarization.Models, then the agent's primary model (appended below
	// as a last-resort fallback). Per-agent models let an agent use specialised
	// summarizers when the default ones refuse its content. See
	// buildCompressLLMClient / buildDefaultCompressLLMClient for per-entry rules.
	var agentModels, globalModels []string
	if agent.Config != nil {
		agentModels = agent.Config.SummarizationModels
	}
	if cfg := al.GetConfig(); cfg != nil {
		globalModels = cfg.Summarization.Models
	}

	var compressClients []llmcontext.LLMClient
	effectiveCompressModel := ""
	chainNames := resolveCompressModelChain(agentModels, globalModels)
	for _, name := range chainNames {
		if effectiveCompressModel == "" {
			effectiveCompressModel = name
		}
		compressClients = append(compressClients, al.buildCompressLLMClient(agent, name, sessionKey))
	}
	// Always append the agent's primary model as the final fallback, so
	// summarization still works when the global list is empty or every
	// configured model fails to produce an acceptable summary.
	compressClients = append(compressClients, al.buildDefaultCompressLLMClient(agent, sessionKey))

	// One clear line (in claw.log) showing the whole compression chain that will
	// be tried in order — the per-client detail above goes to the structured log
	// too, but this is the at-a-glance summary of what's actually in effect.
	logger.InfoCF("llmcontext", "compression model chain", map[string]any{
		"agent_id": agent.ID,
		"session":  sessionKey,
		"chain":    append(append([]string{}, chainNames...), agent.Model+" (agent default)"),
	})

	// Stamp the compaction summary with the effective compress model so the
	// rendered "Generated: <time> by <model>" line is populated (used for
	// debugging compression quality). Falls back to the agent's primary model
	// when no summarization models are configured — that is the model the
	// appended default compress client actually runs against.
	if effectiveCompressModel == "" {
		effectiveCompressModel = strings.TrimSpace(agent.Model)
	}

	// Global debug-capture flag: when on, the manager writes the verbatim
	// request/response of each summarization call to <workspace>/compact.jsonl.
	debugCapture := false
	failureDumpDir := ""
	if cfg := al.GetConfig(); cfg != nil {
		debugCapture = cfg.Summarization.DebugCapture
		if cfg.Logging.DumpFailedCompressions {
			failureDumpDir = al.dumpsDir
		}
	}

	// Reporter delivers the compaction report to the user on the automatic path.
	// Internal channels (e.g. cron-internal) are skipped to avoid loops; manual
	// /compact returns the report directly instead of using this.
	reporter := func(channel, chatID, text string) {
		if text == "" || channel == "" || al.bus == nil || constants.IsInternalChannel(channel) {
			return
		}
		_ = al.bus.PublishOutbound(context.Background(), bus.OutboundMessage{
			Channel: channel,
			ChatID:  chatID,
			Content: text,
		})
	}

	// The archive directory is the sessions directory within the agent workspace.
	// We derive it from the workspace the same way initSessionStore does.
	archiveDir := filepath.Join(agent.Workspace, "sessions")
	opts := append([]llmcontext.Option{
		llmcontext.WithContextWindow(agent.ContextWindow),
		llmcontext.WithArchiveDir(archiveDir),
		llmcontext.WithCompressLLM(compressClients...),
		llmcontext.WithCompressModel(llmcontext.ModelChain{Primary: effectiveCompressModel}),
		llmcontext.WithCompressionProfileDir(agent.Workspace),
		llmcontext.WithCompactDebug(debugCapture),
		llmcontext.WithCompressFailureDumpDir(failureDumpDir),
		llmcontext.WithCompactionReporter(reporter),
		llmcontext.WithCooldownTracker(al.cooldownTracker()),
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

	// Cognitive-memory wiring — cognitive agents ONLY. For every other agent this
	// is a no-op (returns nil) and the manager behaves exactly as before.
	cmCleanup := al.wireCognitiveMemory(agent, sessionKey, cm)

	newEntry := &cmEntry{
		cm:           cm,
		sessionKey:   sessionKey,
		store:        agent.Sessions,
		lastAccessed: time.Now(),
		cleanup:      cmCleanup,
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
		// Release the cogmem store handle we opened for the discarded CM.
		if cmCleanup != nil {
			cmCleanup()
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
