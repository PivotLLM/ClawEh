// ClawEh - Cognitive Memory
// License: MIT

package agent

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/PivotLLM/ClawEh/pkg/cogmem"
	"github.com/PivotLLM/ClawEh/pkg/cogmem/consolidate"
	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// wireCognitiveMemory installs the cognitive-memory archive hook and per-turn
// prompt-injection closure on the ContextManager — but ONLY for cognitive
// agents (those allowed the cogmem tools). For every other agent it returns nil
// immediately and the ContextManager is untouched, preserving identical
// behavior to before.
//
// The returned cleanup func (nil for non-cognitive agents) closes the lazily
// opened per-session cogmem store; the cmEntry calls it on eviction/drain.
func (al *AgentLoop) wireCognitiveMemory(agent *AgentInstance, sessionKey string, cm llmcontext.ContextManager) func() {
	// GATE 1: agent must be allowed the cogmem tools.
	if agent == nil || agent.Config == nil || !agent.Config.CognitiveMemoryEnabled() {
		return nil
	}
	// GATE 2: the concrete manager must expose the wiring setters.
	mgr, ok := cm.(*llmcontext.Manager)
	if !ok {
		return nil
	}

	dbPath := store.SessionDBPath(agent.Workspace, sessionKey)
	archivePath := filepath.Join(agent.Workspace, "sessions",
		store.SanitizeSessionKey(sessionKey)+".archive.db")

	cfg := al.GetConfig()
	if cfg == nil {
		return nil
	}
	mem := cfg.Agents.Defaults.EffectiveMemory(agent.Config)

	// Archive hook: notify the consolidation manager on every archive write.
	al.mu.RLock()
	cogMgr := al.cogmemManager
	al.mu.RUnlock()
	if cogMgr != nil {
		job := consolidate.Job{
			AgentID:     agent.ID,
			SessionKey:  sessionKey,
			Workspace:   agent.Workspace,
			ArchivePath: archivePath,
		}
		mgr.SetArchiveAppendHook(func(_ int64, _ providers.Message) {
			cogMgr.OnMessage(job)
		})
	}

	// Prompt injection: a per-session lazily opened store + composer, guarded by
	// a mutex so concurrent Build calls share one handle. The store is closed by
	// the returned cleanup.
	var (
		mu     sync.Mutex
		st     *store.Store
		comp   *cogmem.Composer
		opened bool
	)

	ensure := func() *cogmem.Composer {
		mu.Lock()
		defer mu.Unlock()
		if opened {
			return comp // may be nil if the open failed
		}
		opened = true
		s, err := store.Open(dbPath)
		if err != nil {
			logger.WarnCF("cogmem", "open session store for prompt injection failed", map[string]any{
				"agent_id":    agent.ID,
				"session_key": sessionKey,
				"path":        dbPath,
				"error":       err.Error(),
			})
			return nil
		}
		st = s
		comp = cogmem.New(s, memoryComposerOptions(mem)...)
		return comp
	}

	mgr.SetMemoryBlocks(func(_ string) (stable, routed string) {
		c := ensure()
		if c == nil {
			return "", ""
		}
		ctx := context.Background()
		s, _, err := c.StableBlock(ctx)
		if err != nil {
			logger.WarnCF("cogmem", "stable block failed", map[string]any{
				"agent_id": agent.ID, "session_key": sessionKey, "error": err.Error(),
			})
		}
		rr, err := c.RoutedBlock(ctx, cogmem.RouteRequest{Trace: mem.Prompt.IncludeDebugTrace})
		if err != nil {
			logger.WarnCF("cogmem", "routed block failed", map[string]any{
				"agent_id": agent.ID, "session_key": sessionKey, "error": err.Error(),
			})
		}
		return s, rr.Text
	})

	return func() {
		mu.Lock()
		defer mu.Unlock()
		if st != nil {
			_ = st.Close()
			st = nil
		}
	}
}

// memoryComposerOptions translates a MemoryConfig into cogmem.Composer options.
func memoryComposerOptions(mem config.MemoryConfig) []cogmem.Option {
	var opts []cogmem.Option
	if mem.Prompt.TopKDomains > 0 {
		opts = append(opts, cogmem.WithTopKDomains(mem.Prompt.TopKDomains))
	}
	if mem.Prompt.MaxChars > 0 {
		opts = append(opts, cogmem.WithMaxChars(mem.Prompt.MaxChars))
	}
	if mem.Prompt.MinConfidence > 0 {
		opts = append(opts, cogmem.WithMinConfidence(mem.Prompt.MinConfidence))
	}
	if mem.Prompt.PendingMax > 0 {
		opts = append(opts, cogmem.WithPendingMax(mem.Prompt.PendingMax))
	}
	if mem.Prompt.PendingSurface != "" {
		opts = append(opts, cogmem.WithPendingSurface(mem.Prompt.PendingSurface))
	}
	return opts
}
