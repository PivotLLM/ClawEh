// ClawEh
// License: MIT

package agent

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/cogmem"
	"github.com/PivotLLM/ctxengine"
	"github.com/PivotLLM/ctxengine/session"

	"github.com/PivotLLM/ClawEh/logger"
)

const (
	defaultEvictTTL      = 2 * time.Hour
	defaultEvictInterval = 30 * time.Minute
)

// cmEntry wraps a ContextManager with lifecycle metadata used by the eviction
// goroutine. The sync.Map in AgentLoop stores *cmEntry values.
type cmEntry struct {
	cm           ctxengine.ContextManager
	sessionKey   string               // used by the eviction pass to revoke session tokens
	store        session.SessionStore // used on eviction to drop per-session in-memory caches
	lastAccessed time.Time
	refcount     atomic.Int32
	// mem is the session's cognitive-memory view; nil for non-cognitive agents.
	// Closed on eviction/drain to release the per-session store handle.
	mem *cogmem.Session
	// token is the per-session MCP token the loop renders into the system
	// prompt on every dispatch (sessionTokenLayer). Reissued on a session
	// reset; "" when no issuer is wired. Guarded by tokenMu.
	tokenMu sync.RWMutex
	token   string
}

// setToken records the current session token.
func (e *cmEntry) setToken(tok string) {
	e.tokenMu.Lock()
	defer e.tokenMu.Unlock()
	e.token = tok
}

// sessionToken returns the current session token, or "".
func (e *cmEntry) sessionToken() string {
	e.tokenMu.RLock()
	defer e.tokenMu.RUnlock()
	return e.token
}

// sessionToken returns the MCP token of a cached session, or "" when the
// session has no entry (a stub injected by a test) or no token was issued.
func (al *AgentLoop) sessionToken(agent *AgentInstance, sessionKey string) string {
	v, _ := al.contextManagers.Load(agent.ID + ":" + sessionKey)
	if entry, ok := v.(*cmEntry); ok {
		return entry.sessionToken()
	}
	return ""
}

// reissueSessionToken revokes nothing itself (the issuer replaces the token
// for the key) but issues a fresh token for the session and stores it on the
// cached entry so the next dispatch renders the new one. No-op without an
// issuer or a cached entry.
func (al *AgentLoop) reissueSessionToken(agent *AgentInstance, sessionKey string) {
	al.mu.RLock()
	sti := al.sessionTokenIssuer
	al.mu.RUnlock()
	if sti == nil {
		return
	}
	v, _ := al.contextManagers.Load(agent.ID + ":" + sessionKey)
	entry, ok := v.(*cmEntry)
	if !ok {
		return
	}
	archiveDir := filepath.Join(agent.Workspace, "sessions")
	if tok := sti.Issue(agent.ID, sessionKey, archiveDir); tok != "" {
		entry.setToken(tok)
	}
}

// forgetSessionState drops per-session in-memory caches in the session store
// (e.g. the noise-dedup cache) when a context manager is evicted, so those
// caches do not grow unbounded over the lifetime of the process. Best-effort:
// stores that do not support it are skipped.
func forgetSessionState(store session.SessionStore, sessionKey string) {
	if store == nil || sessionKey == "" {
		return
	}
	if f, ok := store.(interface{ ForgetSession(sessionKey string) }); ok {
		f.ForgetSession(sessionKey)
	}
}

// evictContextManagers runs until evictStop is closed, waking every
// evictInterval and evicting idle entries.
func (al *AgentLoop) evictContextManagers() {
	ticker := time.NewTicker(al.evictInterval)
	defer ticker.Stop()

	for {
		select {
		case <-al.evictStop:
			return
		case <-ticker.C:
			al.runEvictionPass(al.evictTTL)
		}
	}
}

// dropContextManager force-evicts a single session's context manager (closing
// its DB handles and revoking its token), regardless of idle time, as long as it
// is not in use. Used to tear down an ephemeral sub-agent session right after its
// run so its snapshot DB can be deleted. No-op if absent or still referenced (the
// idle sweep will reclaim it later).
func (al *AgentLoop) dropContextManager(ctx context.Context, agent *AgentInstance, sessionKey string) {
	key := agent.ID + ":" + sessionKey
	v, ok := al.contextManagers.Load(key)
	if !ok {
		return
	}
	entry, ok := v.(*cmEntry)
	if !ok || entry.refcount.Load() > 0 {
		return
	}
	al.contextManagers.Delete(key)

	al.mu.RLock()
	sti := al.sessionTokenIssuer
	al.mu.RUnlock()
	if sti != nil && entry.sessionKey != "" {
		sti.Revoke(entry.sessionKey)
	}
	if err := entry.cm.Close(context.WithoutCancel(ctx)); err != nil {
		logger.WarnCF("agent", "subagent: context manager close failed",
			map[string]any{"key": key, "error": err.Error()})
	}
	forgetSessionState(entry.store, entry.sessionKey)
	entry.mem.Close()
}

// runEvictionPass evicts entries that have refcount == 0 and have been idle
// longer than ttl. It uses a fresh background context so the archive flush
// always completes regardless of the calling goroutine's context.
func (al *AgentLoop) runEvictionPass(ttl time.Duration) {
	al.mu.RLock()
	sti := al.sessionTokenIssuer
	al.mu.RUnlock()

	now := time.Now()
	al.contextManagers.Range(func(key, value any) bool {
		entry, ok := value.(*cmEntry)
		if !ok {
			return true
		}
		if entry.refcount.Load() > 0 {
			return true // in use — skip
		}
		if now.Sub(entry.lastAccessed) < ttl {
			return true // not idle long enough
		}

		// Remove before closing to prevent a concurrent getContextManager from
		// returning the entry while it is being closed.
		al.contextManagers.Delete(key)

		// Revoke the session token so the MCP server no longer accepts calls
		// with the evicted session's token.
		if sti != nil && entry.sessionKey != "" {
			sti.Revoke(entry.sessionKey)
		}

		if err := entry.cm.Close(context.Background()); err != nil {
			logger.WarnCF("agent", "eviction: context manager close failed", map[string]any{
				"key":   key,
				"error": err.Error(),
			})
		}
		forgetSessionState(entry.store, entry.sessionKey)
		entry.mem.Close()
		logger.InfoCF("agent", "evicted idle context manager", map[string]any{
			"key":      key,
			"idle_min": now.Sub(entry.lastAccessed).Minutes(),
		})
		return true
	})
}

// drainContextManagers closes all remaining context managers. Called from
// AgentLoop.Close() after the eviction goroutine has been stopped.
func (al *AgentLoop) drainContextManagers() {
	al.mu.RLock()
	sti := al.sessionTokenIssuer
	al.mu.RUnlock()

	al.contextManagers.Range(func(key, value any) bool {
		entry, ok := value.(*cmEntry)
		if !ok {
			return true
		}
		al.contextManagers.Delete(key)
		if sti != nil && entry.sessionKey != "" {
			sti.Revoke(entry.sessionKey)
		}
		if err := entry.cm.Close(context.Background()); err != nil {
			logger.WarnCF("agent", "shutdown drain: context manager close failed", map[string]any{
				"key":   key,
				"error": err.Error(),
			})
		}
		forgetSessionState(entry.store, entry.sessionKey)
		entry.mem.Close()
		return true
	})
}

// invalidateContextManagers drops every cached ContextManager so the next access
// rebuilds it from the current config. Unlike drainContextManagers (shutdown),
// it does NOT close the manager or revoke session tokens: in-flight holders keep
// their existing entry and active sessions are undisturbed; only the cached
// mapping is cleared so a fresh manager (with the reloaded summarization chain
// and other per-session config) is built on demand. Called on config reload.
func (al *AgentLoop) invalidateContextManagers() {
	n := 0
	al.contextManagers.Range(func(key, _ any) bool {
		al.contextManagers.Delete(key)
		n++
		return true
	})
	if n > 0 {
		logger.DebugCF("agent", "config reload: invalidated cached context managers", map[string]any{"count": n})
	}
}
