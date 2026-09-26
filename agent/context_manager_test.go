// ClawEh
// License: MIT

package agent

import (
	"fmt"
	"sync"
	"testing"
)

// rotatingSTI mimics the real token store's semantics: Issue mints a fresh
// token and invalidates the session's previous one; Revoke drops whatever token
// the session currently has. It counts both so a test can prove that a
// concurrent first access issues exactly one token and revokes none.
type rotatingSTI struct {
	recordingSTI
	mu      sync.Mutex
	issued  int
	revoked int
	live    map[string]string // sessionKey → current token
}

func (s *rotatingSTI) Issue(_, sessionKey, _ string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live == nil {
		s.live = make(map[string]string)
	}
	s.issued++
	tok := fmt.Sprintf("SST-%d", s.issued)
	s.live[sessionKey] = tok
	return tok
}

func (s *rotatingSTI) Revoke(sessionKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked++
	delete(s.live, sessionKey)
}

// TestGetContextManager_ConcurrentFirstAccessKeepsOneLiveToken: many goroutines
// creating the same session's context manager at once must end up sharing one
// entry whose token is the one the issuer still honours. Before the per-key
// build slot, every loser issued its own token (rotating the winner's) and then
// revoked by session key, leaving the winner's prompt carrying a dead token.
func TestGetContextManager_ConcurrentFirstAccessKeepsOneLiveToken(t *testing.T) {
	al := newTestAgentLoop(t).al
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	sti := &rotatingSTI{}
	al.SetSessionTokenIssuer(sti)

	const key = "concurrent-first-access"
	const workers = 32

	var start sync.WaitGroup
	var finished sync.WaitGroup
	start.Add(1)
	var mu sync.Mutex
	seen := make(map[any]int) // distinct ContextManager values handed out
	for range workers {
		finished.Go(func() {
			start.Wait()
			cm, release := al.getContextManager(agent, key)
			defer release()
			mu.Lock()
			seen[cm]++
			mu.Unlock()
		})
	}
	start.Done()
	finished.Wait()

	if len(seen) != 1 {
		t.Fatalf("callers received %d distinct context managers, want 1", len(seen))
	}
	sti.mu.Lock()
	issued, revoked, live := sti.issued, sti.revoked, sti.live[key]
	sti.mu.Unlock()
	if issued != 1 {
		t.Errorf("Issue called %d times, want 1", issued)
	}
	if revoked != 0 {
		t.Errorf("Revoke called %d times during creation, want 0", revoked)
	}
	if got := al.sessionToken(agent, key); got == "" || got != live {
		t.Errorf("entry token %q, issuer's live token %q: they must match", got, live)
	}
	if _, inflight := sessionBuilds.Load(sessionBuildKey{al: al, key: agent.ID + ":" + key}); inflight {
		t.Error("build slot still registered after the build finished")
	}

	// The refcount must reflect every caller having released.
	v, _ := al.contextManagers.Load(agent.ID + ":" + key)
	entry, ok := v.(*cmEntry)
	if !ok {
		t.Fatal("no cached entry after concurrent creation")
	}
	if rc := entry.refcount.Load(); rc != 0 {
		t.Errorf("refcount after all releases = %d, want 0", rc)
	}
}
