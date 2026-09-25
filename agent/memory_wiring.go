// ClawEh - Cognitive Memory
// License: MIT

package agent

import (
	"context"

	"github.com/PivotLLM/cogmem"
	"github.com/PivotLLM/cogmem/consolidate"
	"github.com/PivotLLM/cogmem/store"
	"github.com/PivotLLM/ctxengine"
	"github.com/PivotLLM/ctxengine/memory"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/utils"
)

// wireCognitiveMemory builds the cogmem session for a cognitive agent, or
// returns nil for every other agent (and every method on a nil session is a
// no-op). The cmEntry closes it on eviction/drain.
func (al *AgentLoop) wireCognitiveMemory(agent *AgentInstance, sessionKey string) *cogmem.Session {
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

	mem := cfg.Agents.Defaults.EffectiveMemory(agent.Config)
	perMessageChars := mem.Consolidation.PerMessageChars
	// One memory per agent, shared by every session. A sub-agent works on a
	// throwaway snapshot in its own directory (see runSubagentTask).
	ephemeral := routing.IsSubagentSessionKey(sessionKey)
	dir, id := cogmemhost.Dir(agent.Workspace), agent.ID
	if ephemeral {
		dir, id = cogmemhost.SubagentDir(agent.Workspace, sessionKey), agent.ID+" (sub-agent)"
	}
	return cogmem.NewSession(cogmem.SessionOptions{
		ID:        id,
		Dir:       dir,
		Workspace: agent.Workspace,
		Ephemeral: ephemeral,
		Settings:  cogmemhost.Settings(mem),
		Loader:    cogmemhost.NewLoader(cfg, agent.ID, agent.Workspace),
		Manager:   cogMgr,
		OnOpen: func(ctx context.Context, st *store.Store) {
			backfillInbox(ctx, st, agent.ID, agent.Workspace, sessionKey, perMessageChars)
		},
	})
}

// backfillInbox runs once per store: messages archived before the store kept
// its own inbox, and not yet consolidated, are copied in so the upgrade loses
// nothing to memory. Later opens find the flag set and skip it. Host-side by
// design: only ClawEh knows about the session archive.
func backfillInbox(ctx context.Context, st *store.Store, agentID, workspace, sessionKey string, perMessageChars int) {
	done, err := st.InboxBackfilled(ctx)
	if err != nil || done {
		return
	}
	state, err := st.GetState(ctx, st.DB(), store.InboxStateKey)
	if err != nil {
		return
	}
	copied := 0
	if a, err := memory.OpenReadOnly(archiveDBPath(workspace, sessionKey)); err == nil {
		defer utils.CloseQuietly(a)
		if _, maxSeq, err := a.Bounds(); err == nil && maxSeq > state.ConsolidatedSeq {
			rows, err := a.QueryRange(state.ConsolidatedSeq+1, maxSeq)
			if err == nil {
				for _, r := range rows {
					ok, err := consolidate.Observe(ctx, st, r.Seq, r.Role, r.Content, perMessageChars)
					if err != nil {
						logger.WarnCF("cogmem", "inbox backfill append failed", map[string]any{
							"session_key": sessionKey, "seq": r.Seq, "error": err.Error(),
						})
						return // leave the flag unset so the next open retries
					}
					if ok {
						copied++
					}
				}
			}
		}
	}
	if err := st.SetInboxBackfilled(ctx); err != nil {
		return
	}
	if copied > 0 {
		logger.InfoCF("cogmem", "inbox backfilled from session archive", map[string]any{
			"agent_id": agentID, "session_key": sessionKey, "messages": copied,
		})
	}
}

// recallInjections asks memory for this dispatch's blocks and maps them onto
// the context manager's placements. Nil for agents without memory.
func recallInjections(ctx context.Context, mem *cogmem.Session, routeText string) []ctxengine.Injection {
	rec := mem.Recall(ctx, routeText)
	if len(rec) == 0 {
		return nil
	}
	out := make([]ctxengine.Injection, 0, len(rec))
	for _, r := range rec {
		p := ctxengine.PlaceSystemStable
		if r.Placement == cogmem.PlaceCurrentUser {
			p = ctxengine.PlaceCurrentUser
		}
		out = append(out, ctxengine.Injection{Placement: p, Text: r.Text})
	}
	return out
}
