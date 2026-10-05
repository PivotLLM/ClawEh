// ClawEh
// License: MIT

package agent

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/cogmem"
	"github.com/PivotLLM/ctxengine"
	"github.com/PivotLLM/ctxengine/memory"
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
	cm         ctxengine.ContextManager
	sessionKey string               // used by the eviction pass to revoke session tokens
	store      session.SessionStore // used on eviction to drop per-session in-memory caches
	// lastAccessed is the UnixNano of the most recent access. Atomic because
	// concurrent callers of the same session (and the eviction pass) touch it
	// without any other synchronisation.
	lastAccessed atomic.Int64
	refcount     atomic.Int32
	// stale marks an entry a config reload found in use. It stays in the map
	// so the turn holding it (and anyone sharing it mid-turn) keeps its manager
	// and, above all, the session token already rendered into running prompts;
	// getSessionContext rebuilds it from the new config on the first access
	// after the last holder releases it.
	stale atomic.Bool
	// mem is the session's cognitive-memory view; nil for non-cognitive agents.
	// Closed on eviction/drain to release the per-session store handle.
	mem *cogmem.Session
	// token is the per-session MCP token the loop renders into the system
	// prompt on every dispatch (sessionTokenLayer). Reissued on a session
	// reset; "" when no issuer is wired. Guarded by tokenMu.
	tokenMu sync.RWMutex
	token   string
}

// touch records an access now.
func (e *cmEntry) touch() { e.lastAccessed.Store(time.Now().UnixNano()) }

// idle returns how long the entry has gone unaccessed as of now.
func (e *cmEntry) idle(now time.Time) time.Duration {
	return now.Sub(time.Unix(0, e.lastAccessed.Load()))
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
	if sti == nil || agent.Spec.Fresh {
		return
	}
	v, _ := al.contextManagers.Load(agent.ID + ":" + sessionKey)
	entry, ok := v.(*cmEntry)
	if !ok {
		return
	}
	archiveDir := filepath.Join(agent.StateDir, "sessions")
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
// is not in use. Used to close a temporary agent's session before the agent
// is deleted, and to release a session before its
// archive is deleted. It reports whether the session has no cached manager
// afterwards: false while the entry is referenced or its build slot is busy (the
// idle sweep reclaims it later).
func (al *AgentLoop) dropContextManager(ctx context.Context, agent *AgentInstance, sessionKey, reason string) bool {
	key := agent.ID + ":" + sessionKey
	v, ok := al.contextManagers.Load(key)
	if !ok {
		return true
	}
	entry, ok := v.(*cmEntry)
	if !ok || entry.refcount.Load() > 0 {
		return false
	}
	return al.tryEvictEntry(ctx, key, entry, reason)
}

// runEvictionPass evicts entries that have refcount == 0 and have been idle
// longer than ttl. It uses a fresh background context so the archive flush
// always completes regardless of the calling goroutine's context.
func (al *AgentLoop) runEvictionPass(ttl time.Duration) {
	now := time.Now()
	al.contextManagers.Range(func(key, value any) bool {
		entry, ok := value.(*cmEntry)
		if !ok {
			return true
		}
		if entry.refcount.Load() > 0 {
			return true // in use — skip
		}
		if entry.idle(now) < ttl {
			return true // not idle long enough
		}
		// Losing the refcount race to a new caller leaves the entry stale; it is
		// rebuilt on the first access after release, which is intended.
		al.tryEvictEntry(context.Background(), fmt.Sprint(key), entry, evictReasonIdle)
		return true
	})
}

// Reasons recorded on the eviction log line.
const (
	evictReasonIdle         = "idle"          // TTL pass
	evictReasonReload       = "reload"        // config reload found it unused
	evictReasonStaleRebuild = "stale-rebuild" // first access after a reload-marked entry was released
	evictReasonTempDeleted  = "temp-deleted"  // temporary agent deleted
	evictReasonReleased     = "released"      // session released so its archive can be deleted
	evictReasonSingleShot   = "single-shot"   // a single-shot agent's turn is over
)

// tryEvictEntry evicts entry if nobody holds it, and reports whether it did.
//
// Two things make the eviction safe against concurrent callers of the same key.
// It holds the key's build slot, so no getSessionContext can build, Store or
// Issue for the key meanwhile: the Delete and Revoke in evictEntry can only hit
// this entry and its token, never a successor. And it marks the entry stale
// before reading the refcount, the reverse of the fast path (reference first,
// then the mark), so a concurrent caller is either counted here (the entry
// stays, stale, and is rebuilt on the first access after release) or sees the
// mark and backs off to the build slot. When the slot is busy the eviction is
// skipped; a mark the caller already set is kept.
func (al *AgentLoop) tryEvictEntry(ctx context.Context, key string, entry *cmEntry, reason string) bool {
	bk := sessionBuildKey{al: al, key: key}
	done := make(chan struct{})
	if _, busy := sessionBuilds.LoadOrStore(bk, done); busy {
		return false
	}
	defer func() {
		sessionBuilds.Delete(bk)
		close(done)
	}()

	if v, _ := al.contextManagers.Load(key); v != entry {
		return false // already evicted or replaced
	}
	entry.stale.Store(true)
	if entry.refcount.Load() > 0 {
		return false
	}
	al.evictEntry(ctx, key, entry, reason)
	return true
}

// evictEntry removes one entry from the cache, revokes its session token so the
// MCP server no longer accepts it, and closes its manager and memory session.
// The caller holds the key's build slot and has established, after marking the
// entry stale, that nobody holds it (see tryEvictEntry).
func (al *AgentLoop) evictEntry(ctx context.Context, key string, entry *cmEntry, reason string) {
	al.contextManagers.Delete(key)

	al.mu.RLock()
	sti := al.sessionTokenIssuer
	al.mu.RUnlock()
	if sti != nil && entry.sessionKey != "" {
		sti.Revoke(entry.sessionKey)
	}

	if err := entry.cm.Close(context.WithoutCancel(ctx)); err != nil {
		logger.WarnCF("agent", "eviction: context manager close failed", map[string]any{
			"key":    key,
			"reason": reason,
			"error":  err.Error(),
		})
	}
	forgetSessionState(entry.store, entry.sessionKey)
	entry.mem.Close()
	logger.InfoCF("agent", "evicted context manager", map[string]any{
		"key":      key,
		"reason":   reason,
		"idle_min": entry.idle(time.Now()).Minutes(),
	})
}

// drainContextManagers closes all remaining context managers. Called from
// AgentLoop.Close() after the eviction goroutine has been stopped. The
// managers close concurrently; the context engine's Close does not honour a
// context, so the wait is bounded here instead: when ctx ends first, the
// sessions still closing are logged and left behind.
func (al *AgentLoop) drainContextManagers(ctx context.Context) {
	al.mu.RLock()
	sti := al.sessionTokenIssuer
	al.mu.RUnlock()

	var (
		mu      sync.Mutex
		closing = make(map[string]bool)
		wg      sync.WaitGroup
	)
	al.contextManagers.Range(func(key, value any) bool {
		entry, ok := value.(*cmEntry)
		if !ok {
			return true
		}
		al.contextManagers.Delete(key)
		if sti != nil && entry.sessionKey != "" {
			sti.Revoke(entry.sessionKey)
		}
		name := fmt.Sprint(key)
		mu.Lock()
		closing[name] = true
		mu.Unlock()
		wg.Go(func() {
			if err := entry.cm.Close(context.WithoutCancel(ctx)); err != nil {
				logger.WarnCF("agent", "shutdown drain: context manager close failed", map[string]any{
					"key":   name,
					"error": err.Error(),
				})
			}
			forgetSessionState(entry.store, entry.sessionKey)
			entry.mem.Close()
			mu.Lock()
			delete(closing, name)
			mu.Unlock()
		})
		return true
	})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		mu.Lock()
		busy := slices.Sorted(maps.Keys(closing))
		mu.Unlock()
		logger.WarnCF("agent", "Sessions still busy at shutdown; not waiting for them", map[string]any{"sessions": busy})
	}
}

// invalidateContextManagers makes every cached ContextManager be rebuilt from
// the current config on its next access. Called on config reload.
//
// Every entry is marked stale. One nobody holds is evicted: its token is
// revoked and the next turn builds a fresh manager and renders a fresh token.
// One in use stays in the map, so the turn holding it (and the Maestro workers
// and session tools that carry its token) keeps working; it is rebuilt on the
// first access after the last holder releases it, or by the idle eviction pass.
// An entry whose build slot is busy is left marked for the same treatment.
func (al *AgentLoop) invalidateContextManagers(ctx context.Context) {
	evicted, kept := 0, 0
	al.contextManagers.Range(func(key, value any) bool {
		entry, ok := value.(*cmEntry)
		if !ok {
			al.contextManagers.Delete(key)
			return true
		}
		entry.stale.Store(true)
		if entry.refcount.Load() == 0 && al.tryEvictEntry(ctx, fmt.Sprint(key), entry, evictReasonReload) {
			evicted++
		} else {
			kept++
		}
		return true
	})
	if evicted > 0 || kept > 0 {
		logger.DebugCF("agent", "config reload: invalidated cached context managers",
			map[string]any{"evicted": evicted, "kept_stale": kept})
	}
}

// discardConversation deletes a session's conversation from memory and disk:
// the cached context manager, the store's handle and the archive database. A
// single-shot agent's turn ends with it, so nothing accumulates. It holds the
// session's build slot throughout, so no turn can open the session between
// the eviction and the deletion. While the session is in use (its build slot
// busy, or another turn holding the manager) nothing is deleted; that turn's
// own discard does it, and a single-shot turn starts by clearing the context
// anyway.
// Skipped discard: the database stays on disk (holding at most what the
// holder's turn wrote) until the next single-shot turn or command ends.
func (al *AgentLoop) discardConversation(ctx context.Context, agent *AgentInstance, sessionKey string) {
	key := agent.ID + ":" + sessionKey
	bk := sessionBuildKey{al: al, key: key}
	done := make(chan struct{})
	if _, busy := sessionBuilds.LoadOrStore(bk, done); busy {
		return
	}
	defer func() {
		sessionBuilds.Delete(bk)
		close(done)
	}()

	if v, ok := al.contextManagers.Load(key); ok {
		entry, ok := v.(*cmEntry)
		if !ok {
			return
		}
		entry.stale.Store(true)
		if entry.refcount.Load() > 0 {
			return
		}
		al.evictEntry(ctx, key, entry, evictReasonSingleShot)
	}
	forgetSessionState(agent.Sessions, sessionKey)
	al.releaseSessionPins(sessionKey)
	if err := memory.DeleteSession(filepath.Join(agent.StateDir, "sessions"), sessionKey); err != nil {
		logger.WarnCF("agent", "Failed to delete a single-shot conversation", map[string]any{
			"agent": agent.Label(), "session_key": sessionKey, "error": err.Error(),
		})
	}
}
