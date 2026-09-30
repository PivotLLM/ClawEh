// ClawEh
// License: MIT

package agent

import (
	"context"
	"runtime"
	"sync"
	"testing"
)

// reloadFixture is a real agent loop with a rotating token issuer, for the
// config-reload timing tests: the reload itself is invalidateContextManagers.
type reloadFixture struct {
	t     *testing.T
	al    *AgentLoop
	agent *AgentInstance
	sti   *rotatingSTI
	key   string // session key
}

func newReloadFixture(t *testing.T) *reloadFixture {
	t.Helper()
	al := newTestAgentLoop(t).al
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	sti := &rotatingSTI{}
	al.SetSessionTokenIssuer(sti)
	return &reloadFixture{t: t, al: al, agent: agent, sti: sti, key: "agent:" + agent.ID + ":main"}
}

// acquire takes a reference to the session's context, as a turn does.
func (f *reloadFixture) acquire() func() {
	_, release := f.al.getContextManager(f.agent, f.key)
	return release
}

// entry returns the cached entry for the session, or nil.
func (f *reloadFixture) entry() *cmEntry {
	v, _ := f.al.contextManagers.Load(f.agent.ID + ":" + f.key)
	e, ok := v.(*cmEntry)
	if !ok {
		return nil
	}
	return e
}

// token returns the token the loop renders into the session's prompt.
func (f *reloadFixture) token() string { return f.al.sessionToken(f.agent, f.key) }

func (f *reloadFixture) wantCounts(issued, revoked int) {
	f.t.Helper()
	f.sti.mu.Lock()
	gotIssued, gotRevoked := f.sti.issued, f.sti.revoked
	f.sti.mu.Unlock()
	if gotIssued != issued || gotRevoked != revoked {
		f.t.Fatalf("Issue/Revoke = %d/%d, want %d/%d", gotIssued, gotRevoked, issued, revoked)
	}
}

// wantToken checks the loop renders tok for the session and the issuer still
// honours it.
func (f *reloadFixture) wantToken(tok string) {
	f.t.Helper()
	if tok == "" {
		f.t.Fatal("want a token, got none")
	}
	if got := f.token(); got != tok {
		f.t.Fatalf("sessionToken = %q, want %q", got, tok)
	}
	f.sti.mu.Lock()
	live := f.sti.live[f.key]
	f.sti.mu.Unlock()
	if live != tok {
		f.t.Fatalf("issuer's live token = %q, want %q", live, tok)
	}
}

// TestReload_NoSession: a reload before any session exists touches nothing.
func TestReload_NoSession(t *testing.T) {
	f := newReloadFixture(t)
	f.al.invalidateContextManagers(context.Background())
	f.wantCounts(0, 0)
	if f.entry() != nil {
		t.Fatal("a reload created an entry")
	}
}

// TestReload_IdleSession: a reload while the session is idle evicts the entry
// and revokes its token; the next access builds a new entry with a new token.
func TestReload_IdleSession(t *testing.T) {
	f := newReloadFixture(t)
	f.acquire()()
	before := f.entry()
	tok := f.token()
	f.wantCounts(1, 0)
	f.wantToken(tok)

	f.al.invalidateContextManagers(context.Background())

	if f.entry() != nil {
		t.Fatal("idle entry still cached after reload")
	}
	f.wantCounts(1, 1)
	if f.token() != "" {
		t.Fatal("evicted session still renders a token")
	}

	f.acquire()()
	if after := f.entry(); after == nil || after == before {
		t.Fatal("next access did not build a new entry")
	}
	f.wantCounts(2, 1)
	if f.token() == tok {
		t.Fatal("rebuilt entry kept the revoked token")
	}
	f.wantToken(f.token())
}

// TestReload_TurnInFlight: a reload while a turn holds the entry keeps the
// entry and its token; a concurrent caller (session tools over MCP, mid-turn
// compaction) shares it without rotating the token; after the holder releases,
// the next access rebuilds it.
func TestReload_TurnInFlight(t *testing.T) {
	f := newReloadFixture(t)
	releaseTurn := f.acquire()
	held := f.entry()
	tok := f.token()

	f.al.invalidateContextManagers(context.Background())

	if f.entry() != held {
		t.Fatal("in-use entry left the cache on reload")
	}
	if !held.stale.Load() {
		t.Fatal("in-use entry not marked stale")
	}
	f.wantCounts(1, 0)
	f.wantToken(tok)

	// A concurrent caller for the same key mid-turn shares the entry.
	releaseTool := f.acquire()
	if f.entry() != held {
		t.Fatal("concurrent caller got a different entry")
	}
	f.wantCounts(1, 0)
	f.wantToken(tok)
	releaseTool()
	releaseTurn()

	// Still cached and valid until the next access.
	f.wantToken(tok)

	f.acquire()()
	after := f.entry()
	if after == nil || after == held {
		t.Fatal("stale entry not rebuilt after the holder released it")
	}
	if after.stale.Load() {
		t.Fatal("rebuilt entry is stale")
	}
	f.wantCounts(2, 1)
	if f.token() == tok {
		t.Fatal("rebuilt entry kept the old token")
	}
	f.wantToken(f.token())
}

// TestReload_TwiceDuringOneTurn: two reloads while the same turn is in flight
// keep the entry and token; nothing is revoked until the turn releases.
func TestReload_TwiceDuringOneTurn(t *testing.T) {
	f := newReloadFixture(t)
	releaseTurn := f.acquire()
	held := f.entry()
	tok := f.token()

	f.al.invalidateContextManagers(context.Background())
	f.al.invalidateContextManagers(context.Background())

	if f.entry() != held {
		t.Fatal("in-use entry left the cache")
	}
	f.wantCounts(1, 0)
	f.wantToken(tok)

	releaseTurn()
	f.wantCounts(1, 0)
	f.acquire()()
	if f.entry() == held {
		t.Fatal("stale entry not rebuilt after release")
	}
	f.wantCounts(2, 1)
}

// TestReload_SharedEntry: with two holders at the time of the reload, the
// entry is rebuilt only after the last one releases.
func TestReload_SharedEntry(t *testing.T) {
	f := newReloadFixture(t)
	releaseA := f.acquire()
	releaseB := f.acquire()
	held := f.entry()
	tok := f.token()

	f.al.invalidateContextManagers(context.Background())
	releaseA()

	// B still holds it: another access shares, it does not rebuild.
	f.acquire()()
	if f.entry() != held {
		t.Fatal("entry rebuilt while a holder remained")
	}
	f.wantCounts(1, 0)
	f.wantToken(tok)

	releaseB()
	f.acquire()()
	if f.entry() == held {
		t.Fatal("entry not rebuilt after the last holder released")
	}
	f.wantCounts(2, 1)
}

// TestReload_EvictionPassSkipsHeldStaleEntry: the idle eviction pass leaves a
// stale entry alone while it is held, and evicts it once released.
func TestReload_EvictionPassSkipsHeldStaleEntry(t *testing.T) {
	f := newReloadFixture(t)
	releaseTurn := f.acquire()
	held := f.entry()
	tok := f.token()

	f.al.invalidateContextManagers(context.Background())
	f.al.runEvictionPass(0)

	if f.entry() != held {
		t.Fatal("eviction pass evicted a held stale entry")
	}
	f.wantCounts(1, 0)
	f.wantToken(tok)

	releaseTurn()
	f.al.runEvictionPass(0)
	if f.entry() != nil {
		t.Fatal("released stale entry not evicted by the pass")
	}
	f.wantCounts(1, 1)
}

// TestReload_ConcurrentAccessAfterRelease: many callers reaching a released
// stale entry at once rebuild it exactly once and all share the new entry.
// Run with -race.
func TestReload_ConcurrentAccessAfterRelease(t *testing.T) {
	f := newReloadFixture(t)
	releaseTurn := f.acquire()
	f.al.invalidateContextManagers(context.Background())
	releaseTurn()

	const workers = 32
	var start, done sync.WaitGroup
	start.Add(1)
	var mu sync.Mutex
	seen := make(map[any]int)
	for range workers {
		done.Go(func() {
			start.Wait()
			cm, release := f.al.getContextManager(f.agent, f.key)
			defer release()
			mu.Lock()
			seen[cm]++
			mu.Unlock()
		})
	}
	start.Done()
	done.Wait()

	if len(seen) != 1 {
		t.Fatalf("callers received %d distinct context managers, want 1", len(seen))
	}
	f.wantCounts(2, 1)
	f.wantToken(f.token())
	if rc := f.entry().refcount.Load(); rc != 0 {
		t.Fatalf("refcount after all releases = %d, want 0", rc)
	}
}

// TestReload_ConcurrentAccessWhileHeld: callers racing a reload while a turn
// holds the entry all share it and never rotate its token. Run with -race.
func TestReload_ConcurrentAccessWhileHeld(t *testing.T) {
	f := newReloadFixture(t)
	releaseTurn := f.acquire()
	defer releaseTurn()
	held := f.entry()
	tok := f.token()

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { f.acquire()() })
	}
	wg.Go(func() { f.al.invalidateContextManagers(context.Background()) })
	wg.Wait()

	if f.entry() != held {
		t.Fatal("held entry replaced during concurrent access")
	}
	f.wantCounts(1, 0)
	f.wantToken(tok)
}

// TestReload_ConcurrentWithAccess races reloads against callers taking and
// holding the session, over and over. Whatever the interleaving, a holder's
// manager stays the cached one (so it is neither evicted nor closed while
// held), the token it renders is the one the issuer honours, and only one
// entry ever exists for the key. Run with -race -count=N.
func TestReload_ConcurrentWithAccess(t *testing.T) {
	f := newReloadFixture(t)
	liveToken := func() string {
		f.sti.mu.Lock()
		defer f.sti.mu.Unlock()
		return f.sti.live[f.key]
	}

	for i := range 300 {
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				cm, release := f.al.getContextManager(f.agent, f.key)
				defer release()
				e := f.entry()
				if e == nil || e.cm != cm {
					t.Errorf("iteration %d: the held manager is not the cached one", i)
					return
				}
				if tok := e.sessionToken(); tok == "" || tok != liveToken() {
					t.Errorf("iteration %d: rendered token %q, issuer honours %q", i, tok, liveToken())
				}
				runtime.Gosched()
				if f.entry() != e {
					t.Errorf("iteration %d: entry evicted while held", i)
				}
			})
		}
		wg.Go(func() { f.al.invalidateContextManagers(context.Background()) })
		wg.Wait()
		if t.Failed() {
			return
		}
	}

	n := 0
	f.al.contextManagers.Range(func(_, _ any) bool { n++; return true })
	if n > 1 {
		t.Fatalf("%d cached entries, want at most 1", n)
	}
	f.acquire()()
	e := f.entry()
	if e == nil {
		t.Fatal("no entry after a final access")
	}
	if rc := e.refcount.Load(); rc != 0 {
		t.Fatalf("refcount after all releases = %d, want 0", rc)
	}
	if tok := e.sessionToken(); tok == "" || tok != liveToken() {
		t.Fatalf("entry token %q, issuer honours %q", tok, liveToken())
	}
}
