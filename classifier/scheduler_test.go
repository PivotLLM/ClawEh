// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// eventually waits for cond, failing after a few seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settle waits until the scheduler is idle: waiting on the clock with no
// run in progress.
func (e *env) settle() {
	e.t.Helper()
	eventually(e.t, "the scheduler to settle", func() bool {
		e.svc.mu.Lock()
		running := false
		for _, en := range e.svc.entries {
			running = running || en.running
		}
		e.svc.mu.Unlock()
		return !running && e.clock.Waiters() > 0
	})
}

func (e *env) lists() int { return len(e.tools.callsOf("mail_list")) }

func (e *env) start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	e.svc.Start(ctx)
}

// A classifier runs at start, then every trigger.every_seconds.
func TestSchedulerRunsOnInterval(t *testing.T) {
	e := newEnv(t)
	e.create(definition()) // one live source call
	e.start()
	eventually(t, "the first run", func() bool { return e.lists() == 2 })
	e.settle()
	e.clock.Advance(119 * time.Second)
	e.settle()
	if n := e.lists(); n != 2 {
		t.Fatalf("ran before the interval: %d source calls", n)
	}
	e.clock.Advance(time.Second)
	eventually(t, "the second run", func() bool { return e.lists() == 3 })
	e.settle()
	if got := len(e.moves()); got != 3 {
		t.Errorf("moves = %d", got)
	}
}

// A disabled classifier and an invalid file are not run; the invalid file
// is logged once per change and never deleted.
func TestSchedulerSkipsDisabledAndInvalid(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["enabled"] = false
	e.create(def)
	bad := filepath.Join(e.scope.BaseDirectory, "broken.json")
	if err := os.WriteFile(bad, []byte(`{"id":"broken","owner":"alice","trigger":{"every_seconds":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.start()
	e.settle()
	for range 3 {
		e.clock.Advance(200 * time.Second)
		e.settle()
	}
	if n := e.lists(); n != 1 {
		t.Errorf("source calls = %d, want only create's", n)
	}
	if n := e.log.warnings("broken.json"); n != 1 {
		t.Errorf("invalid file logged %d times, want once", n)
	}
	if _, err := os.Stat(bad); err != nil {
		t.Errorf("the invalid file was removed: %v", err)
	}
	// Fixing the file by hand makes it run.
	good := definition()
	good["id"] = "broken"
	good["owner"] = "alice"
	if err := os.WriteFile(bad, mustJSON(t, good), 0o600); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Second)
	eventually(t, "the fixed file to run", func() bool { return e.lists() == 2 })
	e.settle()
}

// A hand edit of a definition applies at the next run.
func TestSchedulerFollowsHandEdits(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.start()
	eventually(t, "the first run", func() bool { return e.lists() == 2 })
	e.settle()

	path := filepath.Join(e.scope.BaseDirectory, "inbox-priority.json")
	edited := definition()
	edited["owner"] = "alice"
	obj(edited["classify"])["rules"] = []any{"Invoices are urgent."}
	if err := os.WriteFile(path, mustJSON(t, edited), 0o600); err != nil {
		t.Fatal(err)
	}
	e.setInbox([]string{"m9"})
	e.clock.Advance(120 * time.Second)
	eventually(t, "the run after the edit", func() bool { return e.model.count() == 4 })
	e.settle()
	e.model.mu.Lock()
	sys := e.model.systems[len(e.model.systems)-1]
	e.model.mu.Unlock()
	if !strings.Contains(sys, "Rules:\n1. Invoices are urgent.\n") {
		t.Errorf("the run did not use the edited rules:\n%s", sys)
	}
}

// A trigger during a run is coalesced into one more run right after it.
func TestSchedulerCoalescesTriggers(t *testing.T) {
	e := newEnv(t)
	e.setInbox([]string{"m1"})
	def := definition()
	def["trigger"] = map[string]any{"every_seconds": 15}
	e.create(def)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	e.model.setReply(func(string, string) (string, error) {
		entered <- struct{}{}
		<-release
		return "low", nil
	})
	e.start()
	<-entered // the first run is blocked in the model
	eventually(t, "the scheduler to wait", func() bool { return e.clock.Waiters() > 0 })
	for range 3 {
		e.clock.Advance(15 * time.Second)
		eventually(t, "a tick", func() bool { return e.clock.Waiters() > 0 })
	}
	if n := e.lists(); n != 2 {
		t.Fatalf("source calls during the run = %d, want 2", n)
	}
	e.setInbox([]string{"m1"})
	close(release)
	eventually(t, "the coalesced run", func() bool { return e.lists() == 3 })
	e.settle()
	if n := e.lists(); n != 3 {
		t.Errorf("source calls = %d, want exactly one more run", n)
	}
}

// An agent whose switch turns off has its run stopped (the item in flight
// finishes, no more start) and its classifiers kept.
func TestSchedulerStopsWhenSwitchedOff(t *testing.T) {
	e := newEnv(t)
	e.setInbox([]string{"a1", "a2", "a3"})
	e.create(definition())
	release := make(chan struct{})
	entered := make(chan struct{}, 3)
	e.model.setReply(func(string, string) (string, error) {
		entered <- struct{}{}
		<-release
		return "low", nil
	})
	e.start()
	<-entered
	e.mu.Lock()
	e.scopes = nil
	e.mu.Unlock()
	e.svc.Reload()
	eventually(t, "the entry to leave the table", func() bool {
		e.svc.mu.Lock()
		defer e.svc.mu.Unlock()
		for _, en := range e.svc.entries {
			if en.stop != nil {
				return false
			}
		}
		return true
	})
	close(release)
	eventually(t, "the run to end", func() bool {
		rec := e.list(e.scope)
		return len(rec) == 1 && rec[0].LastRun != nil
	})
	list := e.list(e.scope)
	if r := list[0].LastRun; r.Status != RunStopped || r.Classified != 1 {
		t.Errorf("stopped run = %+v", r)
	}
	if n := e.model.count(); n != 1 {
		t.Errorf("model calls = %d, want only the item in flight", n)
	}
	e.clock.Advance(500 * time.Second)
	e.settle()
	if n := e.lists(); n != 2 {
		t.Errorf("source calls after the switch went off = %d", n)
	}
	if _, err := e.svc.Get(e.scope, "inbox-priority"); err != nil {
		t.Errorf("the definition was not kept: %v", err)
	}
}
