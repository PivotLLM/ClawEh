// ClawEh - Cognitive Memory
// License: MIT

package agent

import (
	"context"
	"strings"
	"sync"

	"github.com/PivotLLM/ClawEh/cogmem"
	"github.com/PivotLLM/ClawEh/cogmem/attachfile"
	"github.com/PivotLLM/ClawEh/cogmem/consolidate"
	"github.com/PivotLLM/ClawEh/cogmem/store"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/llmcontext"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/memory"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
)

// maxRecentTools bounds the recent-tool ring used for tool-trigger memory routing.
const maxRecentTools = 8

// memorySession is one agent session's view of cognitive memory. The loop
// hands it a copy of every message (Observe), tells it which tools ran
// (RecordToolUse), and asks it for the blocks to place in the next request
// (Recall). It knows nothing about the context manager, and the context
// manager knows nothing about it: the loop is the only thing that holds both.
//
// A nil *memorySession is valid and does nothing, so callers need no guard for
// agents without cognitive memory.
type memorySession struct {
	agentID    string
	sessionKey string
	workspace  string
	dbPath     string
	// ephemeral is a sub-agent session: its store is a throwaway snapshot, so
	// nothing is fed to consolidation and no inbox is kept.
	ephemeral bool

	mem    config.MemoryConfig
	loader cogmem.AttachmentLoader
	cogMgr *consolidate.Manager

	mu     sync.Mutex
	st     *store.Store
	comp   *cogmem.Composer
	opened bool

	recentMu    sync.Mutex
	recentTools []string
}

// wireCognitiveMemory builds the memory session for a cognitive agent, or
// returns nil for every other agent. The returned session's Close releases the
// lazily opened per-session store; the cmEntry calls it on eviction/drain.
func (al *AgentLoop) wireCognitiveMemory(agent *AgentInstance, sessionKey string) *memorySession {
	if agent == nil || agent.Config == nil || !agent.Config.CognitiveMemoryEnabled() {
		return nil
	}
	cfg := al.GetConfig()
	if cfg == nil {
		return nil
	}
	al.mu.RLock()
	cogMgr := al.cogmemManager
	al.mu.RUnlock()

	return &memorySession{
		agentID:    agent.ID,
		sessionKey: sessionKey,
		workspace:  agent.Workspace,
		dbPath:     store.SessionDBPath(agent.Workspace, sessionKey),
		ephemeral:  routing.IsSubagentSessionKey(sessionKey),
		mem:        cfg.Agents.Defaults.EffectiveMemory(agent.Config),
		loader:     attachfile.NewLoader(cfg, agent.ID, agent.Workspace),
		cogMgr:     cogMgr,
	}
}

// ensure opens the per-session store and composer once, guarded so concurrent
// callers share one handle. Returns nil when the open failed (logged once).
func (m *memorySession) ensure() *store.Store {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.opened {
		return m.st
	}
	m.opened = true
	s, err := store.Open(m.dbPath)
	if err != nil {
		logger.WarnCF("cogmem", "open session store failed", map[string]any{
			"agent_id":    m.agentID,
			"session_key": m.sessionKey,
			"path":        m.dbPath,
			"error":       err.Error(),
		})
		return nil
	}
	m.st = s
	m.comp = cogmem.New(s, append(memoryComposerOptions(m.mem), cogmem.WithAttachmentLoader(m.loader))...)
	if !m.ephemeral {
		m.backfillInbox(s)
	}
	return s
}

// backfillInbox runs once per store: messages archived before the store kept
// its own inbox, and not yet consolidated, are copied in so the upgrade loses
// nothing to memory. Later stores find the flag set and skip it.
func (m *memorySession) backfillInbox(s *store.Store) {
	ctx := context.Background()
	done, err := s.InboxBackfilled(ctx)
	if err != nil || done {
		return
	}
	state, err := s.GetState(ctx, s.DB(), store.InboxStateKey)
	if err != nil {
		return
	}
	copied := 0
	if a, err := memory.OpenReadOnly(archiveDBPath(m.workspace, m.sessionKey)); err == nil {
		defer a.Close()
		if _, maxSeq, err := a.Bounds(); err == nil && maxSeq > state.ConsolidatedSeq {
			rows, err := a.QueryRange(state.ConsolidatedSeq+1, maxSeq)
			if err == nil {
				for _, r := range rows {
					if ok, err := consolidate.Observe(ctx, s, r.Seq, r.Role, r.Content, m.mem.Consolidation.PerMessageChars); err != nil {
						logger.WarnCF("cogmem", "inbox backfill append failed", map[string]any{
							"session_key": m.sessionKey, "seq": r.Seq, "error": err.Error(),
						})
						return // leave the flag unset so the next open retries
					} else if ok {
						copied++
					}
				}
			}
		}
	}
	if err := s.SetInboxBackfilled(ctx); err != nil {
		return
	}
	if copied > 0 {
		logger.InfoCF("cogmem", "inbox backfilled from session archive", map[string]any{
			"agent_id": m.agentID, "session_key": m.sessionKey, "messages": copied,
		})
	}
}

// Observe hands the memory system a copy of a message the loop just stored
// under seq. It lands in the store's inbox for the next consolidation run and
// nudges the run's message-count trigger. Best-effort: a failure is logged and
// never affects the turn.
func (m *memorySession) Observe(ctx context.Context, seq int64, msg providers.Message) {
	if m == nil || m.ephemeral || seq <= 0 {
		return
	}
	s := m.ensure()
	if s == nil {
		return
	}
	stored, err := consolidate.Observe(ctx, s, seq, msg.Role, msg.Content, m.mem.Consolidation.PerMessageChars)
	if err != nil {
		logger.WarnCF("cogmem", "observe message failed", map[string]any{
			"agent_id": m.agentID, "session_key": m.sessionKey, "seq": seq, "error": err.Error(),
		})
		return
	}
	if stored && m.cogMgr != nil {
		m.cogMgr.OnMessage(consolidate.Job{
			AgentID:    m.agentID,
			SessionKey: m.sessionKey,
			Workspace:  m.workspace,
		})
	}
}

// RecordToolUse feeds the recent-tool ring (newest-first, deduped, capped) so
// Recall can auto-load domains whose triggers match a tool the agent just used.
func (m *memorySession) RecordToolUse(names ...string) {
	if m == nil || len(names) == 0 {
		return
	}
	m.recentMu.Lock()
	defer m.recentMu.Unlock()
	for _, n := range names {
		if n == "" {
			continue
		}
		out := m.recentTools[:0]
		for _, e := range m.recentTools {
			if e != n {
				out = append(out, e)
			}
		}
		m.recentTools = append([]string{n}, out...)
	}
	if len(m.recentTools) > maxRecentTools {
		m.recentTools = m.recentTools[:maxRecentTools]
	}
}

func (m *memorySession) recentToolsSnapshot() []string {
	m.recentMu.Lock()
	defer m.recentMu.Unlock()
	if len(m.recentTools) == 0 {
		return nil
	}
	out := make([]string, len(m.recentTools))
	copy(out, m.recentTools)
	return out
}

// Recall returns the blocks to place in the next request: the STABLE block
// (sticky domains, topic index, their attached documents) for the system
// message, and the ROUTED block (domains selected from routeText and the recent
// tools, with their documents) for the current turn. Nil when there is nothing
// to inject or the store could not be opened.
func (m *memorySession) Recall(ctx context.Context, routeText string) []llmcontext.Injection {
	if m == nil || m.ensure() == nil {
		return nil
	}
	res, err := m.comp.Compose(ctx, cogmem.RouteRequest{
		RecentTools: m.recentToolsSnapshot(),
		RouteText:   routeText,
		Trace:       m.mem.Prompt.IncludeDebugTrace,
	})
	if err != nil {
		logger.WarnCF("cogmem", "memory compose failed", map[string]any{
			"agent_id": m.agentID, "session_key": m.sessionKey, "error": err.Error(),
		})
	}
	if res.Attachments != "" || res.RoutedAttachments != "" {
		// Split by provenance: sticky bytes ride in the cached prompt and are
		// paid for once, routed bytes ride with the turn and are paid for
		// every time. One combined figure hides which is which.
		logger.DebugCF("cogmem", "attached documents injected", map[string]any{
			"agent_id":     m.agentID,
			"session_key":  m.sessionKey,
			"sticky_bytes": len(res.Attachments),
			"routed_bytes": len(res.RoutedAttachments),
		})
	}
	// Sticky documents belong with the stable block; routed documents belong
	// with the routed block, whose memory ids their headers cite.
	var out []llmcontext.Injection
	if stable := joinBlocks(res.Stable, res.Attachments); stable != "" {
		out = append(out, llmcontext.Injection{Placement: llmcontext.PlaceSystemStable, Text: stable})
	}
	if routed := joinBlocks(res.Routed, res.RoutedAttachments); routed != "" {
		out = append(out, llmcontext.Injection{Placement: llmcontext.PlaceCurrentUser, Text: routed})
	}
	return out
}

// Close releases the store handle. Safe on nil and when never opened.
func (m *memorySession) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.st != nil {
		_ = m.st.Close()
		m.st = nil
		m.comp = nil
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
