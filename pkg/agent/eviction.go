// ClawEh
// License: MIT

package agent

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/session"
)

const (
	defaultEvictTTL      = 2 * time.Hour
	defaultEvictInterval = 30 * time.Minute
)

// cmEntry wraps a ContextManager with lifecycle metadata used by the eviction
// goroutine. The sync.Map in AgentLoop stores *cmEntry values.
type cmEntry struct {
	cm           llmcontext.ContextManager
	sessionKey   string               // used by the eviction pass to revoke session tokens
	store        session.SessionStore // used on eviction to drop per-session in-memory caches
	lastAccessed time.Time
	refcount     atomic.Int32
	// cleanup, when non-nil, releases per-entry resources on eviction/drain
	// (e.g. the cached cognitive-memory store handle). Nil for non-cognitive
	// agents.
	cleanup func()
}

// forgetSessionState drops per-session in-memory caches in the session store
// (e.g. the noise-dedup cache) when a context manager is evicted, so those
// caches do not grow unbounded over the lifetime of the process. Best-effort:
// stores that do not support it are skipped.
func forgetSessionState(store session.SessionStore, sessionKey string) {
	if store == nil || sessionKey == "" {
		return
	}
	if f, ok := store.(interface{ ForgetSession(string) }); ok {
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
		if entry.cleanup != nil {
			entry.cleanup()
		}
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
		if entry.cleanup != nil {
			entry.cleanup()
		}
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
