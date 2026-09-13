// ClawEh - Personal AI Assistant
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	cogmemstore "github.com/PivotLLM/ClawEh/cogmem/store"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/memory"
)

// compactionStateStore is the subset of the session store used to persist the
// per-session active model index inside the CompactionState record.
type compactionStateStore interface {
	GetCompactionState(sessionKey string) (memory.CompactionState, error)
	SetCompactionState(sessionKey string, state memory.CompactionState) error
}

// activeModelCacheKey builds the cache key for a session's active model index.
func activeModelCacheKey(agentID, sessionKey string) string {
	return agentID + "\x00" + sessionKey
}

// getActiveModelIndex returns the session's active model index, loading it from
// the session store on a cache miss. The result is clamped to a valid candidate
// index and cached.
func (al *AgentLoop) getActiveModelIndex(agent *AgentInstance, sessionKey string) int {
	key := activeModelCacheKey(agent.ID, sessionKey)

	al.activeModelMu.Lock()
	if idx, ok := al.activeModelIdx[key]; ok {
		al.activeModelMu.Unlock()
		return idx
	}
	al.activeModelMu.Unlock()

	idx := 0
	if store, ok := agent.Sessions.(compactionStateStore); ok {
		if st, err := store.GetCompactionState(sessionKey); err == nil {
			idx = st.ActiveModelIndex
		}
	}
	// Clamp to the valid candidate range.
	if n := len(agent.Candidates); n == 0 {
		idx = 0
	} else if idx < 0 {
		idx = 0
	} else if idx >= n {
		idx = n - 1
	}

	al.activeModelMu.Lock()
	al.activeModelIdx[key] = idx
	al.activeModelMu.Unlock()
	return idx
}

// setActiveModelIndex sets the session's active model index, updating the cache
// and persisting it through the session store's CompactionState. Persistence is
// best-effort: a store error is logged but does not fail the call.
func (al *AgentLoop) setActiveModelIndex(agent *AgentInstance, sessionKey string, idx int) error {
	n := len(agent.Candidates)
	if n == 0 {
		return fmt.Errorf("this agent has no selectable models")
	}
	if idx < 0 || idx >= n {
		return fmt.Errorf("model index out of range (0-%d)", n-1)
	}

	key := activeModelCacheKey(agent.ID, sessionKey)
	al.activeModelMu.Lock()
	al.activeModelIdx[key] = idx
	al.activeModelMu.Unlock()

	if store, ok := agent.Sessions.(compactionStateStore); ok {
		st, err := store.GetCompactionState(sessionKey)
		if err != nil {
			logger.WarnCF("agent", "active model index: load compaction state failed",
				map[string]any{"session_key": sessionKey, "error": err.Error()})
			return nil
		}
		st.ActiveModelIndex = idx
		if err := store.SetCompactionState(sessionKey, st); err != nil {
			logger.WarnCF("agent", "active model index: persist failed",
				map[string]any{"session_key": sessionKey, "error": err.Error()})
		}
	}
	return nil
}

// getExposeReasoning returns whether the session exposes the model's reasoning
// to the user, loading it from the session store on a cache miss. Default false.
func (al *AgentLoop) getExposeReasoning(agent *AgentInstance, sessionKey string) bool {
	key := activeModelCacheKey(agent.ID, sessionKey)

	al.exposeReasoningMu.Lock()
	if v, ok := al.exposeReasoningCache[key]; ok {
		al.exposeReasoningMu.Unlock()
		return v
	}
	al.exposeReasoningMu.Unlock()

	v := false
	if store, ok := agent.Sessions.(compactionStateStore); ok {
		if st, err := store.GetCompactionState(sessionKey); err == nil {
			v = st.ExposeReasoning
		}
	}

	al.exposeReasoningMu.Lock()
	al.exposeReasoningCache[key] = v
	al.exposeReasoningMu.Unlock()
	return v
}

// setExposeReasoning sets the session's expose-reasoning flag, updating the cache
// and persisting it through CompactionState (best-effort).
func (al *AgentLoop) setExposeReasoning(agent *AgentInstance, sessionKey string, v bool) {
	key := activeModelCacheKey(agent.ID, sessionKey)
	al.exposeReasoningMu.Lock()
	al.exposeReasoningCache[key] = v
	al.exposeReasoningMu.Unlock()

	if store, ok := agent.Sessions.(compactionStateStore); ok {
		st, err := store.GetCompactionState(sessionKey)
		if err != nil {
			logger.WarnCF("agent", "expose reasoning: load compaction state failed",
				map[string]any{"session_key": sessionKey, "error": err.Error()})
			return
		}
		st.ExposeReasoning = v
		if err := store.SetCompactionState(sessionKey, st); err != nil {
			logger.WarnCF("agent", "expose reasoning: persist failed",
				map[string]any{"session_key": sessionKey, "error": err.Error()})
		}
	}
}

// ToolActivityLine renders the one-line "/tools on" breadcrumb for a tool call
// dispatched over the MCP path (a CLI provider hitting claw's MCP server), or ""
// when the session has tool activity off, the agent is unknown, or the tool
// produces no summary. Wired into the MCP server as a ToolActivityNotifier so
// CLI-routed tool calls surface to the user the same way loop-dispatched ones do.
func (al *AgentLoop) ToolActivityLine(agentID, sessionKey, toolName string, args map[string]any) string {
	agent, ok := al.registry.GetAgent(agentID)
	if !ok || agent == nil {
		return ""
	}
	if !al.getShowToolActivity(agent, sessionKey) {
		return ""
	}
	return toolActivitySummary(toolName, args)
}

// getShowToolActivity returns whether the session posts a one-line breadcrumb for
// each tool call, loading it from the session store on a cache miss. Default false.
func (al *AgentLoop) getShowToolActivity(agent *AgentInstance, sessionKey string) bool {
	key := activeModelCacheKey(agent.ID, sessionKey)

	al.showToolActivityMu.Lock()
	if v, ok := al.showToolActivityCache[key]; ok {
		al.showToolActivityMu.Unlock()
		return v
	}
	al.showToolActivityMu.Unlock()

	v := false
	if store, ok := agent.Sessions.(compactionStateStore); ok {
		if st, err := store.GetCompactionState(sessionKey); err == nil {
			v = st.ShowToolActivity
		}
	}

	al.showToolActivityMu.Lock()
	al.showToolActivityCache[key] = v
	al.showToolActivityMu.Unlock()
	return v
}

// setShowToolActivity sets the session's show-tool-activity flag, updating the
// cache and persisting it through CompactionState (best-effort).
func (al *AgentLoop) setShowToolActivity(agent *AgentInstance, sessionKey string, v bool) {
	key := activeModelCacheKey(agent.ID, sessionKey)
	al.showToolActivityMu.Lock()
	al.showToolActivityCache[key] = v
	al.showToolActivityMu.Unlock()

	if store, ok := agent.Sessions.(compactionStateStore); ok {
		st, err := store.GetCompactionState(sessionKey)
		if err != nil {
			logger.WarnCF("agent", "show tool activity: load compaction state failed",
				map[string]any{"session_key": sessionKey, "error": err.Error()})
			return
		}
		st.ShowToolActivity = v
		if err := store.SetCompactionState(sessionKey, st); err != nil {
			logger.WarnCF("agent", "show tool activity: persist failed",
				map[string]any{"session_key": sessionKey, "error": err.Error()})
		}
	}
}

// cogmemSessionStatus renders a short cognitive-memory summary for the session:
// active domain/memory counts, the pending-review count, and the last
// consolidation run. Returns "" when the agent is not cognitive. Opening the
// store runs the normal idempotent migrations, matching the cogmem_status tool.
func (al *AgentLoop) cogmemSessionStatus(agent *AgentInstance, sessionKey string) string {
	if agent == nil || agent.Config == nil || !agent.Config.CognitiveMemoryEnabled() {
		return ""
	}
	path := cogmemstore.SessionDBPath(agent.Workspace, sessionKey)
	if _, err := os.Stat(path); err != nil {
		return "No cognitive-memory database for this session yet."
	}
	s, err := cogmemstore.Open(path)
	if err != nil {
		return "Cognitive memory unavailable: " + err.Error()
	}
	defer s.Close()

	ctx := context.Background()
	db := s.DB()
	var b strings.Builder

	active, _ := s.ListDomains(ctx, db, cogmemstore.StatusActive)
	memCount := 0
	for _, d := range active {
		ms, _ := s.ListMemories(ctx, db, d.ID, cogmemstore.StatusActive)
		memCount += len(ms)
	}
	fmt.Fprintf(&b, "Active domains: %d\n", len(active))
	fmt.Fprintf(&b, "Active memories: %d\n", memCount)

	run, ok, _ := s.LastRun(ctx, db)
	if !ok {
		b.WriteString("Last consolidation: none yet")
	} else {
		when := run.StartedAt.Format("2006-01-02 15:04")
		fmt.Fprintf(&b, "Last consolidation: %s — trigger=%s, status=%s, changes=%d",
			when, run.Trigger, run.Status, run.OpsApplied)
		if run.Error != "" {
			fmt.Fprintf(&b, "\nLast error: %s", run.Error)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
