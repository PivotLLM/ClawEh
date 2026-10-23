// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The release delay is drawn in [releaseDelayMin, releaseDelayMax] and is
// not a constant.
func TestReleaseDelayRange(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 1000 {
		d := randomReleaseDelay()
		if d < releaseDelayMin || d > releaseDelayMax {
			t.Fatalf("release delay %v outside [%v, %v]", d, releaseDelayMin, releaseDelayMax)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Errorf("release delay is constant: %v", seen)
	}
}

// A controller uses the system clock, the one-second poll and the random
// release delay unless the host or a test sets them.
func TestControllerTimingDefaults(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlReleaseLayer()))
	f.host.Clock = nil
	c := f.open()
	if _, ok := c.clock.(systemClock); !ok {
		t.Errorf("clock = %T, want the system clock", c.clock)
	}
	ctlWant(t, "poll", c.cooldownPoll, time.Second)
	if d := c.releaseDelay(); d < releaseDelayMin || d > releaseDelayMax {
		t.Errorf("release delay %v outside [%v, %v]", d, releaseDelayMin, releaseDelayMax)
	}
}

func ctlReleaseLayer() Layer {
	return Layer{ID: "one", Participants: []string{"alice"}, Instructions: "LAYER-one", Delivery: DeliveryAfterRound, MaxRounds: 1, Output: Output{Format: FormatText}}
}

// A held turn waits the release delay after the cooldown ends before it is
// sent, within its call timeout.
func TestCtlReleaseDelayAfterHold(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlReleaseLayer()))
	until := f.clock.Now().Add(100 * time.Millisecond)
	ctlCooldown(f, until)
	const delay = 300 * time.Millisecond
	var draws atomic.Int32
	f.release = func() time.Duration { draws.Add(1); return delay }
	f.driveClock(100 * time.Millisecond)
	var sentAt time.Time
	f.msg.respond = func(cl ctlCall) (Reply, error) {
		sentAt = f.clock.Now()
		return f.reply(cl), nil
	}
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	calls := f.msg.all()
	ctlWant(t, "calls", len(calls), 1)
	ctlWant(t, "draws", draws.Load(), int32(1))
	ctlWant(t, "sent at", sentAt, until.Add(delay))
	ctlWant(t, "wait", calls[0].Wait, 60*time.Second-100*time.Millisecond-delay)
}

// A turn that was not held is sent without a release delay.
func TestCtlNoReleaseDelayWithoutHold(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlReleaseLayer()))
	f.host.Cooldown = func(string) (string, time.Duration) { return "", 0 }
	var draws atomic.Int32
	f.release = func() time.Duration { draws.Add(1); return time.Hour }
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	ctlWant(t, "calls", len(f.msg.all()), 1)
	ctlWant(t, "draws", draws.Load(), int32(0))
}

// A cancel during the release delay ends it at its next poll, long before
// the delay, without the turn being sent.
func TestCtlReleaseDelayInterruptedByCancel(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlReleaseLayer()))
	ctlCooldown(f, f.clock.Now().Add(50*time.Millisecond))
	delaying := make(chan struct{})
	var once sync.Once
	f.release = func() time.Duration {
		once.Do(func() { close(delaying) })
		return 30 * time.Second
	}
	c := f.open()
	done := make(chan Status)
	go func() {
		st, err := c.Run(context.Background())
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- st
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := f.clock.BlockUntil(ctx, 1); err != nil { // the hold
		t.Fatalf("the turn was never held: %v", err)
	}
	f.clock.Advance(50 * time.Millisecond)
	<-delaying
	if err := f.clock.BlockUntil(ctx, 1); err != nil { // the delay's first poll
		t.Fatalf("the release delay never waited: %v", err)
	}
	start := f.clock.Now()
	if err := c.RequestCancel(); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(c.cooldownPoll)
	ctlWant(t, "status", <-done, StatusCancelled)
	ctlWant(t, "delay waited", f.clock.Since(start), time.Second)
	ctlWant(t, "calls", len(f.msg.all()), 0)
}

// A release delay that would leave less than minHeldWait of the call
// timeout makes the attempt a timeout without waiting it or sending; the
// next attempt, not held, is sent.
func TestCtlReleaseDelayLeavingNoTime(t *testing.T) {
	cfg := ctlConfig(ctlReleaseLayer())
	cfg.Limits.CallTimeoutSeconds = 2
	f := ctlLaunch(t, cfg)
	start := f.clock.Now()
	ctlCooldown(f, start.Add(100*time.Millisecond))
	var draws atomic.Int32
	f.release = func() time.Duration { draws.Add(1); return 1500 * time.Millisecond }
	f.driveClock(100 * time.Millisecond)
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	ctlWant(t, "held for", f.clock.Since(start), 100*time.Millisecond) // the delay was not waited
	calls := f.msg.all()
	ctlWant(t, "calls", len(calls), 1)
	ctlWant(t, "wait", calls[0].Wait, 2*time.Second)
	ctlWant(t, "draws", draws.Load(), int32(1))
	att := f.attempts("one")
	ctlWant(t, "attempts", len(att), 2)
	ctlWant(t, "first outcome", att[0].Reply.Outcome, OutcomeTimeout)
	ctlWant(t, "second outcome", att[1].Reply.Outcome, OutcomeOK)
}

// A model back in cooldown during the release delay holds the turn again;
// it is sent after the next release, on one attempt.
func TestCtlReleaseDelayCooldownAgain(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlReleaseLayer()))
	var mu sync.Mutex
	checks := 0
	f.host.Cooldown = func(agentID string) (string, time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		checks++
		switch {
		case agentID != "alice":
			return "", 0
		case checks == 1: // the hold starts
			return "slow-model", 20 * time.Millisecond
		case checks == 3: // after the first release delay
			return "slow-model", 20 * time.Millisecond
		}
		return "", 0
	}
	var draws atomic.Int32
	f.release = func() time.Duration { draws.Add(1); return 50 * time.Millisecond }
	f.driveClock(10 * time.Millisecond)
	start := f.clock.Now()
	var sentAt time.Time
	f.msg.respond = func(cl ctlCall) (Reply, error) {
		sentAt = f.clock.Now()
		return f.reply(cl), nil
	}
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	ctlWant(t, "calls", len(f.msg.all()), 1)
	ctlWant(t, "draws", draws.Load(), int32(2))
	ctlWant(t, "attempts", len(f.attempts("one")), 1)
	// Two holds of 20ms, each followed by a 50ms release delay.
	ctlWant(t, "sent after", sentAt.Sub(start), 140*time.Millisecond)
}
