// ClawEh
// License: MIT

package agent

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"

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
		// Build the compress client using the same provider as the primary agent.
		// The provider handles model routing internally via the model string.
		compressClient := &providerLLMClient{provider: agent.Provider, model: compressModelName}
		// compress client is tried first; primary model is the fallback.
		compressClients = []llmcontext.LLMClient{compressClient, llmClient}
		log.Printf("agent: compress_model resolved to %q for agent %q session %q",
			compressModelName, agent.ID, sessionKey)
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
