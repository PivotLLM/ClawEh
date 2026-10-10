// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/internal/clock"
	"github.com/PivotLLM/ClawEh/logger"
)

// The backoff before retrying a timed-out dispatch is waited out on the
// loop's clock: it ends only once that clock has passed it, and at once when
// the turn's context ends.
func TestWaitToRetry_OnTheClock(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	fc := clock.NewFake(time.Now())
	done := make(chan error, 1)
	go func() { done <- waitToRetry(context.Background(), fc, errors.New("timed out"), 0) }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := fc.BlockUntil(ctx, 1); err != nil {
		t.Fatalf("the backoff never waited on the clock: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("returned before the clock moved: %v", err)
	default:
	}
	fc.Advance(2 * timeoutRetryBackoff) // past the jittered first step
	if err := <-done; err != nil {
		t.Fatalf("waitToRetry = %v, want nil", err)
	}
	if fc.Waiters() != 0 {
		t.Fatalf("waiters = %d after the wait", fc.Waiters())
	}

	ended, stop := context.WithCancel(context.Background())
	stop()
	if err := waitToRetry(ended, fc, errors.New("timed out"), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("with an ended context = %v, want context.Canceled", err)
	}
}

// The agent registry runs on the loop's clock: a temporary agent idle past
// its TTL on that clock is deleted by the sweeper the loop starts.
func TestRegistry_SweepsOnTheLoopClock(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	tl := newTestAgentLoop(t)
	al := tl.al
	fc := fakeClock(al)
	reg := al.GetRegistry()
	id, err := reg.CreateClone("main", agentreg.EphemeralMemory())
	if err != nil {
		t.Fatalf("CreateClone: %v", err)
	}

	stop := make(chan struct{})
	swept := make(chan struct{})
	go func() {
		defer close(swept)
		reg.RunSweeper(stop, agentreg.SweepInterval)
	}()
	defer func() { close(stop); <-swept }()

	advanceWhenWaiting(t, fc, 1, agentreg.DefaultTTL)
	// Every further sweep sees the agent idle past its TTL.
	deadline := time.Now().Add(30 * time.Second)
	for {
		fc.Advance(agentreg.SweepInterval)
		time.Sleep(10 * time.Millisecond)
		if _, ok := reg.Get(id); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the idle temporary agent was not swept on the loop's clock")
		}
	}
}

// The session pruner runs on the loop's clock: dispatch state idle for an
// hour on that clock is dropped.
func TestPruneSessions_OnTheLoopClock(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	al := &AgentLoop{evictStop: make(chan struct{}), evictInterval: time.Minute}
	fc := fakeClock(al)
	al.sessions = map[string]*sessionState{"agent:alice:main": {lastUsed: fc.Now()}}
	done := make(chan struct{})
	go func() { defer close(done); al.pruneSessions() }()
	defer func() { close(al.evictStop); <-done }()

	advanceWhenWaiting(t, fc, 1, sessionIdleTTL)
	deadline := time.Now().Add(30 * time.Second)
	for {
		fc.Advance(al.evictInterval)
		time.Sleep(10 * time.Millisecond)
		al.sessionsMu.Lock()
		n := len(al.sessions)
		al.sessionsMu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("idle session state was not pruned on the loop's clock")
		}
	}
}
