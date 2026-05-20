// ClawEh
// License: MIT

package agent

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

const (
	defaultEvictTTL      = 2 * time.Hour
	defaultEvictInterval = 30 * time.Minute
)

// cmEntry wraps a ContextManager with lifecycle metadata used by the eviction
// goroutine. The sync.Map in AgentLoop stores *cmEntry values.
type cmEntry struct {
	cm           llmcontext.ContextManager
	sessionKey   string // used by the eviction pass to revoke session tokens
	lastAccessed time.Time
	refcount     atomic.Int32
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
		return true
	})
}
