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

// ctlReleaseDelay replaces releaseDelay for the test.
func ctlReleaseDelay(t *testing.T, fn func() time.Duration) {
	t.Helper()
	prev := releaseDelay
	releaseDelay = fn
	t.Cleanup(func() { releaseDelay = prev })
}

// The release delay is drawn in [releaseDelayMin, releaseDelayMax] and is
// not a constant.
func TestReleaseDelayRange(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 1000 {
		d := releaseDelay()
		if d < releaseDelayMin || d > releaseDelayMax {
			t.Fatalf("release delay %v outside [%v, %v]", d, releaseDelayMin, releaseDelayMax)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Errorf("release delay is constant: %v", seen)
	}
}

func ctlReleaseLayer() Layer {
	return Layer{ID: "one", Participants: []string{"alice"}, Instructions: "LAYER-one", Delivery: DeliveryAfterRound, MaxRounds: 1, Output: Output{Format: FormatText}}
}

// A held turn waits the release delay after the cooldown ends before it is
// sent, within its call timeout.
func TestCtlReleaseDelayAfterHold(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlReleaseLayer()))
	until := time.Now().Add(100 * time.Millisecond)
	ctlCooldown(t, f, until)
	const delay = 300 * time.Millisecond
	var draws atomic.Int32
	ctlReleaseDelay(t, func() time.Duration { draws.Add(1); return delay })
	var sentAt time.Time
	f.msg.respond = func(cl ctlCall) (Reply, error) {
		sentAt = time.Now()
		return f.reply(cl), nil
	}
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	calls := f.msg.all()
	ctlWant(t, "calls", len(calls), 1)
	ctlWant(t, "draws", draws.Load(), int32(1))
	if early := until.Add(delay).Sub(sentAt); early > 0 {
		t.Errorf("the turn was sent %v before the release delay ended", early)
	}
	if limit := 60 * time.Second; calls[0].Wait >= limit-delay || calls[0].Wait < limit-10*time.Second {
		t.Errorf("wait = %v, want what is left of the 60s call timeout after the hold and the delay", calls[0].Wait)
	}
}

// A turn that was not held is sent without a release delay.
func TestCtlNoReleaseDelayWithoutHold(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlReleaseLayer()))
	f.host.Cooldown = func(string) (string, time.Duration) { return "", 0 }
	var draws atomic.Int32
	ctlReleaseDelay(t, func() time.Duration { draws.Add(1); return time.Hour })
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	ctlWant(t, "calls", len(f.msg.all()), 1)
	ctlWant(t, "draws", draws.Load(), int32(0))
}

// A cancel during the release delay ends it at once, without the turn being
// sent.
func TestCtlReleaseDelayInterruptedByCancel(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlReleaseLayer()))
	ctlCooldown(t, f, time.Now().Add(50*time.Millisecond))
	delaying := make(chan struct{})
	var once sync.Once
	ctlReleaseDelay(t, func() time.Duration {
		once.Do(func() { close(delaying) })
		return 30 * time.Second
	})
	c := f.open()
	done := make(chan Status)
	go func() {
		st, err := c.Run(context.Background())
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- st
	}()
	<-delaying
	start := time.Now()
	if err := c.RequestCancel(); err != nil {
		t.Fatal(err)
	}
	ctlWant(t, "status", <-done, StatusCancelled)
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the cancel took %v to end the release delay", took)
	}
	ctlWant(t, "calls", len(f.msg.all()), 0)
}

// A release delay that would leave less than minHeldWait of the call
// timeout makes the attempt a timeout without waiting it or sending; the
// next attempt, not held, is sent.
func TestCtlReleaseDelayLeavingNoTime(t *testing.T) {
	cfg := ctlConfig(ctlReleaseLayer())
	cfg.Limits.CallTimeoutSeconds = 2
	f := ctlLaunch(t, cfg)
	ctlCooldown(t, f, time.Now().Add(100*time.Millisecond))
	var draws atomic.Int32
	ctlReleaseDelay(t, func() time.Duration { draws.Add(1); return 1500 * time.Millisecond })
	start := time.Now()
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	if took := time.Since(start); took >= 1500*time.Millisecond {
		t.Errorf("the run took %v: the release delay was waited", took)
	}
	calls := f.msg.all()
	ctlWant(t, "calls", len(calls), 1)
	if calls[0].Wait <= 0 {
		t.Errorf("the ask was sent with wait %v", calls[0].Wait)
	}
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
	ctlCooldown(t, f, time.Time{})
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
	ctlReleaseDelay(t, func() time.Duration { draws.Add(1); return 50 * time.Millisecond })
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	ctlWant(t, "calls", len(f.msg.all()), 1)
	ctlWant(t, "draws", draws.Load(), int32(2))
	ctlWant(t, "attempts", len(f.attempts("one")), 1)
}
