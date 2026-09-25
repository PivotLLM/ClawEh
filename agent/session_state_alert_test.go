// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/PivotLLM/ctxengine/memory"
	"github.com/PivotLLM/ctxengine/session"
	"github.com/tenebris-tech/alerter"
)

type alertRecorder struct {
	mu     sync.Mutex
	alerts []alerter.Alert
}

func (r *alertRecorder) Send(a alerter.Alert) {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
}
func (r *alertRecorder) Normal(string, string, ...string)    {}
func (r *alertRecorder) Urgent(string, string, ...string)    {}
func (r *alertRecorder) Emergency(string, string, ...string) {}
func (r *alertRecorder) Close(context.Context) error         { return nil }

// failingCompactionStore is the agent's real session store with a compaction
// state that can still be read but no longer written.
type failingCompactionStore struct {
	session.SessionStore
	inner compactionStateStore
}

func (f failingCompactionStore) GetCompactionState(sessionKey string) (memory.CompactionState, error) {
	return f.inner.GetCompactionState(sessionKey)
}

func (failingCompactionStore) SetCompactionState(string, memory.CompactionState) error {
	return errors.New("disk full")
}

// TestSessionState_AlertsWhenPersistFails: each per-session setting whose
// persist fails raises a low alert naming the setting, keyed by the session
// store; a working store raises none.
func TestSessionState_AlertsWhenPersistFails(t *testing.T) {
	al := newTestAgentLoop(t).al
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	agent.Candidates = fourCandidates()
	rec := &alertRecorder{}
	al.SetAlerter(rec)

	if err := al.setActiveModelIndex(agent, "s", 1); err != nil {
		t.Fatal(err)
	}
	al.setExposeReasoning(agent, "s", true)
	al.setShowToolActivity(agent, "s", true)
	if len(rec.alerts) != 0 {
		t.Fatalf("working store must not alert, got %+v", rec.alerts)
	}

	inner, ok := agent.Sessions.(compactionStateStore)
	if !ok {
		t.Fatalf("session store %T does not persist compaction state", agent.Sessions)
	}
	agent.Sessions = failingCompactionStore{SessionStore: agent.Sessions, inner: inner}
	if err := al.setActiveModelIndex(agent, "s", 2); err != nil {
		t.Fatal(err)
	}
	al.setExposeReasoning(agent, "s", false)
	al.setShowToolActivity(agent, "s", false)

	want := []string{"active model index", "expose reasoning", "show tool activity"}
	if len(rec.alerts) != len(want) {
		t.Fatalf("want %d alerts, got %+v", len(want), rec.alerts)
	}
	for i, a := range rec.alerts {
		if a.Priority != alerter.Normal || a.EventID != "session-store" || a.Title != "Session state not persisted" ||
			a.Description != want[i]+": the setting reverts on restart" || a.Details != "disk full" {
			t.Fatalf("alert %d: want low alert for %q, got %+v", i, want[i], a)
		}
	}
}
