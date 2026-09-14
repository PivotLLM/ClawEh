// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/PivotLLM/cogmem"
	"github.com/PivotLLM/spawnllm/openai_compat"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/cronmsg"
	"github.com/PivotLLM/ClawEh/dump"
	"github.com/PivotLLM/ClawEh/llmcontext"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

// providerLLMClient is one resolved provider+model pair of the summarization
// chain; compressModelCaller walks a list of them.
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

// chat dispatches one call to the resolved provider, requesting a JSON-object
// response format when jsonObject is set.
func (c *providerLLMClient) chat(ctx context.Context, messages []providers.Message, jsonObject bool) (*providers.LLMResponse, error) {
	var opts map[string]any
	if jsonObject {
		opts = map[string]any{
			openai_compat.ResponseFormatJSONObjectOption: true,
		}
	}
	return c.provider.Chat(ctx, messages, nil, c.model, opts)
}

// compressModelCaller is the host's llmcontext.ModelCaller: it walks the
// agent's summarization chain in order (agent summarization_models → global
// summarization.models → the agent's primary model), skipping models the
// engine excluded and models the shared cooldown tracker has parked, and
// returns the first reply together with the model that produced it. A
// transport error moves on to the next model and marks the failure against
// the cooldown policy, so an out-of-credits summarizer is not hammered on
// every compaction; the last error is returned when every model fails.
type compressModelCaller struct {
	clients  []*providerLLMClient
	cooldown *providers.CooldownTracker
	// agentID and sessionKey label the log lines.
	agentID, sessionKey string
}

// Complete implements llmcontext.ModelCaller.
func (c *compressModelCaller) Complete(ctx context.Context, req llmcontext.ModelRequest) (llmcontext.ModelReply, error) {
	content, finishReason, model, err := c.complete(ctx, req.System, req.User, req.JSONObject, req.Exclude)
	return llmcontext.ModelReply{Content: content, FinishReason: finishReason, Model: model}, err
}

// complete is the chain walk itself, in plain types so another caller shape
// (the cognitive-memory consolidation model) can wrap it without an import.
// It returns the reply and the model that served it; on failure the model is
// the last one tried (or "" when nothing was), so a report can still name it.
func (c *compressModelCaller) complete(ctx context.Context, system, user string, jsonObject bool, exclude []string) (content, finishReason, model string, err error) {
	messages := []providers.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}
	var lastErr error
	lastModel := ""
	excluded, cooling := 0, 0
	for _, cl := range c.clients {
		if cl.model != "" && slices.Contains(exclude, cl.model) {
			excluded++
			continue
		}
		if c.cooldown != nil && cl.model != "" && !c.cooldown.IsAvailable(cl.providerName, cl.model) {
			cooling++
			logger.InfoCF("llmcontext", "compression model in cooldown; skipping", map[string]any{
				"agent_id":  c.agentID,
				"session":   c.sessionKey,
				"model":     cl.model,
				"remaining": c.cooldown.CooldownRemaining(cl.providerName, cl.model).Round(time.Second).String(),
			})
			continue
		}
		resp, chatErr := cl.chat(ctx, messages, jsonObject)
		if chatErr != nil {
			lastErr, lastModel = chatErr, cl.model
			// Record the failure against the shared cooldown policy; statuses
			// that never cool (413, no HTTP status) are ignored internally.
			if fe := providers.ClassifyError(chatErr, "", cl.model); fe != nil && c.cooldown != nil {
				c.cooldown.MarkFailure(cl.providerName, cl.model, fe.Reason, fe.Status, fe.RetryAfter)
			}
			logger.WarnCF("llmcontext", "compression model call failed; trying next in chain", map[string]any{
				"agent_id": c.agentID,
				"session":  c.sessionKey,
				"model":    cl.model,
				"error":    chatErr.Error(),
			})
			continue
		}
		if c.cooldown != nil {
			c.cooldown.MarkSuccess(cl.providerName, cl.model) // reset any prior cooldown/escalation
		}
		return resp.Content, resp.FinishReason, cl.model, nil
	}
	if lastErr != nil {
		return "", "", lastModel, lastErr
	}
	return "", "", "", fmt.Errorf("%w: %d excluded, %d in cooldown", llmcontext.ErrNoModel, excluded, cooling)
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

// resolveCompressClient resolves one summarization model to a concrete client
// for the chain walker.
func (al *AgentLoop) resolveCompressClient(agent *AgentInstance, compressModelName, sessionKey string) *providerLLMClient {
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

// resolveDefaultCompressClient resolves the agent's primary model to a concrete
// client, the chain walker's last resort.
func (al *AgentLoop) resolveDefaultCompressClient(agent *AgentInstance, sessionKey string) *providerLLMClient {
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

// newCompressModelCaller builds the summarization ModelCaller for an agent and
// session: the agent's own summarization_models first, then the global
// cfg.Summarization.Models, then the agent's primary model as a last-resort
// fallback so summarization still works when the lists are empty or every
// configured model fails. Cooldowns are shared with the main fallback chain
// so a model parked by either path (e.g. an out-of-credits 402) is skipped by
// both. Also returns the effective (first) model name for the summary stamp.
func (al *AgentLoop) newCompressModelCaller(agent *AgentInstance, sessionKey string) (*compressModelCaller, string) {
	var agentModels, globalModels []string
	if agent.Config != nil {
		agentModels = agent.Config.SummarizationModels
	}
	if cfg := al.GetConfig(); cfg != nil {
		globalModels = cfg.Summarization.Models
	}

	var clients []*providerLLMClient
	effective := ""
	chainNames := resolveCompressModelChain(agentModels, globalModels)
	for _, name := range chainNames {
		if effective == "" {
			effective = name
		}
		clients = append(clients, al.resolveCompressClient(agent, name, sessionKey))
	}
	clients = append(clients, al.resolveDefaultCompressClient(agent, sessionKey))

	// One clear line (in claw.log) showing the whole compression chain that will
	// be tried in order — the per-client detail above goes to the structured log
	// too, but this is the at-a-glance summary of what's actually in effect.
	logger.InfoCF("llmcontext", "compression model chain", map[string]any{
		"agent_id": agent.ID,
		"session":  sessionKey,
		"chain":    append(append([]string{}, chainNames...), agent.Model+" (agent default)"),
	})

	// The rendered "Generated: <time> by <model>" line names the effective
	// compress model; it falls back to the agent's primary model when no
	// summarization models are configured — that is the model the appended
	// default client actually runs against.
	if effective == "" {
		effective = strings.TrimSpace(agent.Model)
	}
	return &compressModelCaller{
		clients:    clients,
		cooldown:   al.cooldownTracker(),
		agentID:    agent.ID,
		sessionKey: sessionKey,
	}, effective
}

// getContextManager returns the ContextManager for the given agent+session
// pair. See getSessionContext; this is the form for callers that do not touch
// memory (compact, clear, session info).
func (al *AgentLoop) getContextManager(agent *AgentInstance, sessionKey string) (llmcontext.ContextManager, func()) {
	cm, _, release := al.getSessionContext(agent, sessionKey)
	return cm, release
}

// getSessionContext returns the ContextManager and the cognitive-memory session
// for the given agent+session pair, creating and caching them on first access.
// Both are shared across all calls for the same (agentID, sessionKey) tuple;
// the memory session is nil for agents without cognitive memory.
//
// The returned release function must be deferred by the caller to decrement the
// reference count. The eviction goroutine skips entries with refcount > 0.
func (al *AgentLoop) getSessionContext(agent *AgentInstance, sessionKey string) (llmcontext.ContextManager, *cogmem.Session, func()) {
	key := agent.ID + ":" + sessionKey

	// Fast path: entry already exists.
	if v, ok := al.contextManagers.Load(key); ok {
		entry := v.(*cmEntry)
		entry.refcount.Add(1)
		entry.lastAccessed = time.Now()
		release := func() { entry.refcount.Add(-1) }
		return entry.cm, entry.mem, release
	}

	// Slow path: create a new ContextManager and wrap it in a cmEntry.

	// The summarization model chain. Per-agent models let an agent use
	// specialised summarizers when the default ones refuse its content; see
	// resolveCompressClient / resolveDefaultCompressClient for per-entry rules.
	caller, effectiveCompressModel := al.newCompressModelCaller(agent, sessionKey)

	// Global debug-capture flag: when on, the manager writes the verbatim
	// request/response of each summarization call to <workspace>/compact.jsonl.
	// Failed summarization attempts are dumped to logs/dumps when enabled.
	debugCapture := false
	var failureDump llmcontext.FailureDumpFunc
	if cfg := al.GetConfig(); cfg != nil {
		debugCapture = cfg.Summarization.DebugCapture
		if cfg.Logging.DumpFailedCompressions && al.dumpsDir != "" {
			dumpsDir := al.dumpsDir
			failureDump = func(kind string, meta map[string]any, input, output string) error {
				_, err := dump.Write(dumpsDir, kind, meta, json.RawMessage(input), json.RawMessage(output))
				return err
			}
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
		llmcontext.WithModelCaller(caller),
		llmcontext.WithCompressModel(llmcontext.ModelChain{Primary: effectiveCompressModel}),
		llmcontext.WithCompressionProfileDir(agent.Workspace),
		llmcontext.WithCompactDebug(debugCapture),
		llmcontext.WithFailureDump(failureDump),
		llmcontext.WithCompactionReporter(reporter),
		// Repeated fires of one scheduled job differ only by timestamp; the
		// engine collapses them by the cron collapse key.
		llmcontext.WithNoiseKey(cronmsg.CollapseKey),
	}, agent.CompressOpts...)
	cm := llmcontext.New(sessionKey, agent.Sessions, opts...)

	// Issue a session token so session-scoped MCP tools can identify this session.
	// The loop renders it into the system prompt (sessionTokenLayer) on every
	// dispatch, so it lives on the entry.
	al.mu.RLock()
	sti := al.sessionTokenIssuer
	al.mu.RUnlock()
	token := ""
	if sti != nil {
		token = sti.Issue(agent.ID, sessionKey, archiveDir)
	}

	// Cognitive-memory session — cognitive agents ONLY; nil for every other
	// agent, and every method on a nil session is a no-op.
	mem := al.wireCognitiveMemory(agent, sessionKey)

	newEntry := &cmEntry{
		cm:           cm,
		sessionKey:   sessionKey,
		store:        agent.Sessions,
		lastAccessed: time.Now(),
		mem:          mem,
	}
	newEntry.setToken(token)
	newEntry.refcount.Store(1)

	actual, loaded := al.contextManagers.LoadOrStore(key, newEntry)
	if loaded {
		// Another goroutine beat us; use theirs and discard ours.
		// The one we created (cm) is not stored and will be GC'd.
		// Revoke the token we just issued since we won't use this CM.
		if sti != nil {
			sti.Revoke(sessionKey)
		}
		// Release the cogmem store handle we may have opened for the discarded CM.
		mem.Close()
		entry := actual.(*cmEntry)
		entry.refcount.Add(1)
		entry.lastAccessed = time.Now()
		release := func() { entry.refcount.Add(-1) }
		return entry.cm, entry.mem, release
	}

	release := func() { newEntry.refcount.Add(-1) }
	return newEntry.cm, newEntry.mem, release
}
