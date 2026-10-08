// ClawEh
// License: MIT

package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ctxengine/session"

	"github.com/PivotLLM/ClawEh/logger"
)

// closeCountingStore counts the Close calls on a session store.
type closeCountingStore struct {
	session.SessionStore
	closes atomic.Int32
}

func (s *closeCountingStore) Close() error {
	s.closes.Add(1)
	return s.SessionStore.Close()
}

// A config reload closes the session store of every config instance it
// replaces once nothing uses it, including the context manager a turn built
// on it; the store is not closed twice and the agent keeps working.
func TestReload_ClosesReplacedConfigInstanceStore(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()

	tl := newTestAgentLoop(t)
	al := tl.al
	reg := al.GetRegistry()
	agent, ok := reg.Get(reg.DefaultID())
	if !ok {
		t.Fatal("no default agent")
	}
	counting := &closeCountingStore{SessionStore: agent.Sessions}
	agent.Sessions = counting

	if _, err := al.ProcessDirect(context.Background(), "hello", "direct-session"); err != nil {
		t.Fatalf("ProcessDirect: %v", err)
	}
	for range 3 {
		if err := al.ReloadProviderAndConfig(context.Background(), tl.provider, tl.cfg); err != nil {
			t.Fatalf("ReloadProviderAndConfig: %v", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for counting.closes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := counting.closes.Load(); n != 1 {
		t.Fatalf("replaced instance's store closed %d times, want 1", n)
	}
	if _, err := al.ProcessDirect(context.Background(), "hello again", "direct-session"); err != nil {
		t.Fatalf("ProcessDirect after reloads: %v", err)
	}
}

// An agent is named by its trimmed name, or its id when it has none.
func TestAgentInstance_DisplayName(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Alice", "Alice"},
		{"  Alice  ", "Alice"},
		{"   ", "alice"},
		{"", "alice"},
	} {
		if got := (&AgentInstance{ID: "alice", Name: tc.name}).DisplayName(); got != tc.want {
			t.Errorf("DisplayName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
