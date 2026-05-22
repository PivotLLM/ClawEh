// ClawEh
// License: MIT

package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// trackingContextManager is a stub ContextManager that records whether
// Close has been called. Used to verify eviction behaviour.
type trackingContextManager struct {
	closed atomic.Bool
}

func (m *trackingContextManager) AddUserMessage(_ context.Context, _ providers.Message) error {
	return nil
}
func (m *trackingContextManager) AddAssistantMessage(_ context.Context, _ providers.Message) error {
	return nil
}
func (m *trackingContextManager) AddToolCallMessage(_ context.Context, _ providers.Message) error {
	return nil
}
func (m *trackingContextManager) AddToolResult(_ context.Context, _ providers.Message) error {
	return nil
}
func (m *trackingContextManager) PreDispatchCheck(_ context.Context, current []providers.Message) ([]providers.Message, error) {
	return current, nil
}
func (m *trackingContextManager) CheckAndCompress(_ context.Context, built []providers.Message) ([]providers.Message, error) {
	return built, nil
}
func (m *trackingContextManager) SetSystemPrompt(_ string)   {}
func (m *trackingContextManager) SetCallContext(_, _ string) {}
func (m *trackingContextManager) SetSessionToken(_ string)   {}
func (m *trackingContextManager) Build(_ context.Context) ([]providers.Message, error) {
	return nil, nil
}
func (m *trackingContextManager) Compact(_ context.Context) error    { return nil }
func (m *trackingContextManager) ForceCompress(_ context.Context) error { return nil }
func (m *trackingContextManager) Stats() llmcontext.ContextStats      { return llmcontext.ContextStats{} }
func (m *trackingContextManager) Reset(_ context.Context) error       { return nil }
func (m *trackingContextManager) Close(_ context.Context) error {
	m.closed.Store(true)
	return nil
}

// makeEntry is a test helper that inserts a cmEntry directly into the sync.Map.
func makeEntry(al *AgentLoop, key string, cm llmcontext.ContextManager, lastAccessed time.Time, refcount int32) *cmEntry {
	entry := &cmEntry{
		cm:           cm,
		lastAccessed: lastAccessed,
	}
	entry.refcount.Store(refcount)
	al.contextManagers.Store(key, entry)
	return entry
}

// TestEviction_IdleEntryEvicted verifies that an entry with refcount==0 and
// idle time > TTL is evicted and Close is called.
func TestEviction_IdleEntryEvicted(t *testing.T) {
	al := &AgentLoop{
		evictStop:     make(chan struct{}),
		evictTTL:      defaultEvictTTL,
		evictInterval: defaultEvictInterval,
	}

	cm := &trackingContextManager{}
	oldTime := time.Now().Add(-3 * time.Hour) // well past the 2-hour TTL
	makeEntry(al, "agent:sess1", cm, oldTime, 0)

	al.runEvictionPass(defaultEvictTTL)

	if !cm.closed.Load() {
		t.Error("expected Close to be called on idle evicted entry")
	}

	// Verify the entry was removed from the map.
	if _, ok := al.contextManagers.Load("agent:sess1"); ok {
		t.Error("expected entry to be removed from contextManagers after eviction")
	}
}

// TestEviction_ActiveEntryNotEvicted verifies that an entry with refcount > 0
// is not evicted even when idle time exceeds TTL.
func TestEviction_ActiveEntryNotEvicted(t *testing.T) {
	al := &AgentLoop{
		evictStop:     make(chan struct{}),
		evictTTL:      defaultEvictTTL,
		evictInterval: defaultEvictInterval,
	}

	cm := &trackingContextManager{}
	oldTime := time.Now().Add(-3 * time.Hour)
	makeEntry(al, "agent:active", cm, oldTime, 1) // refcount=1: in use

	al.runEvictionPass(defaultEvictTTL)

	if cm.closed.Load() {
		t.Error("Close must not be called on an entry with active refcount")
	}

	if _, ok := al.contextManagers.Load("agent:active"); !ok {
		t.Error("active entry must remain in contextManagers")
	}
}

// TestEviction_RecentEntryNotEvicted verifies that an entry that was accessed
// recently is not evicted even with refcount==0.
func TestEviction_RecentEntryNotEvicted(t *testing.T) {
	al := &AgentLoop{
		evictStop:     make(chan struct{}),
		evictTTL:      defaultEvictTTL,
		evictInterval: defaultEvictInterval,
	}

	cm := &trackingContextManager{}
	recentTime := time.Now().Add(-30 * time.Minute) // within 2-hour TTL
	makeEntry(al, "agent:recent", cm, recentTime, 0)

	al.runEvictionPass(defaultEvictTTL)

	if cm.closed.Load() {
		t.Error("Close must not be called on a recently-accessed entry")
	}
}

// TestDrainContextManagers verifies that Close() drains all entries.
func TestDrainContextManagers(t *testing.T) {
	al := &AgentLoop{
		evictStop:     make(chan struct{}),
		evictTTL:      defaultEvictTTL,
		evictInterval: defaultEvictInterval,
	}

	cms := make([]*trackingContextManager, 5)
	for i := range cms {
		cms[i] = &trackingContextManager{}
		makeEntry(al, "agent:drain"+string(rune('0'+i)), cms[i], time.Now(), 0)
	}

	al.drainContextManagers()

	for i, cm := range cms {
		if !cm.closed.Load() {
			t.Errorf("entry %d not closed after drain", i)
		}
	}
}

// TestGetContextManager_RefcountLifecycle verifies that getContextManager
// increments the refcount and the release function decrements it.
func TestGetContextManager_RefcountLifecycle(t *testing.T) {
	al := &AgentLoop{
		evictStop:     make(chan struct{}),
		evictTTL:      defaultEvictTTL,
		evictInterval: defaultEvictInterval,
	}

	cm := &trackingContextManager{}
	entry := makeEntry(al, "agent:reftest", cm, time.Now(), 0)

	// Simulate getContextManager by loading the existing entry.
	// Since getContextManager creates a new ContextManager from AgentInstance
	// (which requires a real agent), we test the refcount mechanics directly
	// via the entry.
	entry.refcount.Add(1)
	if entry.refcount.Load() != 1 {
		t.Fatalf("expected refcount 1, got %d", entry.refcount.Load())
	}

	// Release.
	entry.refcount.Add(-1)
	if entry.refcount.Load() != 0 {
		t.Fatalf("expected refcount 0 after release, got %d", entry.refcount.Load())
	}

	// With refcount==0 and stale time, eviction should fire.
	entry.lastAccessed = time.Now().Add(-3 * time.Hour)
	al.runEvictionPass(defaultEvictTTL)
	if !cm.closed.Load() {
		t.Error("expected eviction after refcount drops to 0 and TTL exceeded")
	}
}

// TestEviction_ConcurrentAccess verifies concurrent eviction passes and
// getContextManager-equivalent operations don't race. Run with -race.
func TestEviction_ConcurrentAccess(t *testing.T) {
	al := &AgentLoop{
		evictStop:     make(chan struct{}),
		evictTTL:      time.Millisecond, // very short TTL so eviction fires
		evictInterval: defaultEvictInterval,
	}

	const n = 50
	var wg sync.WaitGroup

	// Spawn goroutines that insert entries.
	for i := range n {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cm := &trackingContextManager{}
			makeEntry(al, "concurrent:"+string(rune('a'+id%26)), cm,
				time.Now().Add(-5*time.Millisecond), 0)
		}(i)
	}

	// Spawn goroutines that run eviction passes concurrently.
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			al.runEvictionPass(time.Millisecond)
		}()
	}

	wg.Wait()
	// No panics or data races = pass.
}
