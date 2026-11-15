// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ctxengine/memory"
	"github.com/PivotLLM/ctxengine/session"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/alerts"
	"github.com/PivotLLM/ClawEh/internal/clock"
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

// TestSessionState_AlertsWhenPersistFails: a per-session setting whose
// persist fails raises a low alert naming the setting, keyed by the session
// store; a working store raises none. A store that refuses one setting
// refuses the next, so the settings that follow do not alert again until a
// day has passed or a setting was stored in between.
func TestSessionState_AlertsWhenPersistFails(t *testing.T) {
	al := newTestAgentLoop(t).al
	fc := clock.NewFake(time.Date(2026, time.November, 14, 9, 2, 0, 0, time.UTC))
	al.clock = fc
	agent := al.registry.Default()
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

	working := agent.Sessions
	inner, ok := working.(compactionStateStore)
	if !ok {
		t.Fatalf("session store %T does not persist compaction state", working)
	}
	failing := failingCompactionStore{SessionStore: working, inner: inner}
	agent.Sessions = failing
	if err := al.setActiveModelIndex(agent, "s", 2); err != nil {
		t.Fatal(err)
	}
	al.setExposeReasoning(agent, "s", false)
	al.setShowToolActivity(agent, "s", false)

	if len(rec.alerts) != 1 {
		t.Fatalf("three settings refused by one store are one alert, got %+v", rec.alerts)
	}
	a := rec.alerts[0]
	if a.Priority != alerter.Normal || a.EventID != "session-store" || a.Title != "Session state not persisted" ||
		a.Description != "active model index: the setting reverts on restart" || a.Details != "disk full" {
		t.Fatalf("want a low alert for the first setting refused, got %+v", a)
	}

	// A day later the store still refuses: one reminder, with the count.
	fc.Advance(alerts.RemindEvery)
	al.setExposeReasoning(agent, "s", true)
	if len(rec.alerts) != 2 || rec.alerts[1].Description !=
		"expose reasoning: the setting reverts on restart; ongoing since Nov 14 09:02, 2 more since the last alert" {
		t.Fatalf("a day later the store must alert once more with the count, got %+v", rec.alerts)
	}

	// A setting that is stored ends it: the next refusal alerts at once.
	agent.Sessions = working
	al.setShowToolActivity(agent, "s", true)
	agent.Sessions = failing
	al.setShowToolActivity(agent, "s", false)
	if len(rec.alerts) != 3 || rec.alerts[2].Description != "show tool activity: the setting reverts on restart" {
		t.Fatalf("a refusal after a stored setting must alert as new, got %+v", rec.alerts)
	}
}

// TestStoreFaultKey: a configured agent's store is its own, a clone's counts
// as its source's and every other temporary agent's as one, so sub-agents and
// forum participants on a full disk do not alert once each.
func TestStoreFaultKey(t *testing.T) {
	configured := &AgentInstance{ID: "alice"}
	clone := &AgentInstance{ID: "1a2b3c4d", Spec: agentreg.Spec{ID: "1a2b3c4d", Origin: agentreg.OriginTemp, SourceID: "alice"}}
	fresh1 := &AgentInstance{ID: "u1", Spec: agentreg.Spec{ID: "u1", Origin: agentreg.OriginTemp}}
	fresh2 := &AgentInstance{ID: "u2", Spec: agentreg.Spec{ID: "u2", Origin: agentreg.OriginTemp}}

	if !clone.Spec.IsClone() || !fresh1.IsTemp() || fresh1.Spec.IsClone() {
		t.Fatal("the fixtures are not a clone and a fresh temporary agent")
	}
	const title = "Session not saved"
	if storeFaultKey(clone, title) != storeFaultKey(configured, title) {
		t.Error("a clone's store must count as its source's")
	}
	if storeFaultKey(fresh1, title) != storeFaultKey(fresh2, title) {
		t.Error("temporary agents that are not clones must share one key")
	}
	if storeFaultKey(fresh1, title) == storeFaultKey(configured, title) {
		t.Error("a temporary agent's store is not a configured agent's")
	}
	if storeFaultKey(configured, title) == storeFaultKey(configured, "Session store write failed") {
		t.Error("two alerts about one store are two keys")
	}
	if storeFaultKey(configured, title) == storeFaultKey(&AgentInstance{ID: "bob"}, title) {
		t.Error("two configured agents have two stores")
	}
}
