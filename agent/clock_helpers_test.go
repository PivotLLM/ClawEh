// ClawEh
// License: MIT

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/internal/clock"
)

// fakeClock puts a fake clock, reading the current time, on al and returns
// it. Call it before al runs anything.
func fakeClock(al *AgentLoop) *clock.Fake {
	fc := clock.NewFake(time.Now())
	al.clock = fc
	return fc
}

// loopClock is the fake clock fakeClock put on al.
func loopClock(t *testing.T, al *AgentLoop) *clock.Fake {
	t.Helper()
	fc, ok := al.clock.(*clock.Fake)
	if !ok {
		t.Fatal("the loop runs on the real clock")
	}
	return fc
}

// advanceWhenWaiting waits until n timers wait on fc, then moves it d on.
func advanceWhenWaiting(t *testing.T, fc *clock.Fake, n int, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := fc.BlockUntil(ctx, n); err != nil {
		t.Fatalf("waiting for %d timer(s) on the clock: %v (%d waiting)", n, err, fc.Waiters())
	}
	fc.Advance(d)
}

// waitSignals returns an onWait hook and the channel it reports each wait of
// kind on.
func waitSignals(kind waitKind) (func(waitKind), <-chan struct{}) {
	ch := make(chan struct{}, 16)
	return func(k waitKind) {
		if k == kind {
			ch <- struct{}{}
		}
	}, ch
}

// awaitSignal fails the test unless ch reports within a generous bound.
func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
