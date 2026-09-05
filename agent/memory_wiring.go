// ClawEh - Cognitive Memory
// License: MIT

package agent

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PivotLLM/ClawEh/cogmem"
	"github.com/PivotLLM/ClawEh/cogmem/attachfile"
	"github.com/PivotLLM/ClawEh/cogmem/consolidate"
	"github.com/PivotLLM/ClawEh/cogmem/store"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/llmcontext"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
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

	// Retention guard: protect not-yet-consolidated archive messages from
	// pruning for cognitive agents when configured (default true).
	mgr.SetProtectUnconsolidated(mem.Retention.ProtectUnconsolidated)

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
		opts := append(memoryComposerOptions(mem),
			cogmem.WithAttachmentLoader(attachfile.NewLoader(cfg, agent.ID, agent.Workspace)))
		comp = cogmem.New(s, opts...)
		return comp
	}

	mgr.SetMemoryBlocks(func(_ string, recentTools []string, routeText string) (stable, routed string) {
		c := ensure()
		if c == nil {
			return "", ""
		}
		res, err := c.Compose(context.Background(), cogmem.RouteRequest{
			RecentTools: recentTools,
			RouteText:   routeText,
			Trace:       mem.Prompt.IncludeDebugTrace,
		})
		if err != nil {
			logger.WarnCF("cogmem", "memory compose failed", map[string]any{
				"agent_id": agent.ID, "session_key": sessionKey, "error": err.Error(),
			})
		}
		if res.Attachments != "" || res.RoutedAttachments != "" {
			// Split by provenance: sticky bytes ride in the cached prompt and are
			// paid for once, routed bytes ride with the turn and are paid for
			// every time. One combined figure hides which is which.
			logger.DebugCF("cogmem", "attached documents injected", map[string]any{
				"agent_id":     agent.ID,
				"session_key":  sessionKey,
				"sticky_bytes": len(res.Attachments),
				"routed_bytes": len(res.RoutedAttachments),
			})
		}
		// Sticky documents belong with the stable block; routed documents belong
		// with the routed block, whose memory ids their headers cite.
		return joinBlocks(res.Stable, res.Attachments), joinBlocks(res.Routed, res.RoutedAttachments)
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
	if mem.Prompt.FileMaxBytes > 0 {
		opts = append(opts, cogmem.WithFileMaxBytes(mem.Prompt.FileMaxBytes))
	}
	if mem.Prompt.FileTotalMaxBytes > 0 {
		opts = append(opts, cogmem.WithFileTotalMaxBytes(mem.Prompt.FileTotalMaxBytes))
	}
	return opts
}

// joinBlocks concatenates non-empty prompt blocks with the separator used
// throughout the system prompt.
func joinBlocks(blocks ...string) string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b != "" {
			out = append(out, b)
		}
	}
	return strings.Join(out, "\n\n---\n\n")
}
