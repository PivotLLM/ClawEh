package channels

import (
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/internal/clock"
)

// recordingClock is a fake clock that records every wait asked of After.
// When instant, a wait completes at once and moves the clock by its length,
// so retry loops run without waiting; otherwise the wait is the fake's and
// the test moves the clock.
type recordingClock struct {
	*clock.Fake
	instant bool

	mu    sync.Mutex
	waits []time.Duration
}

func newRecordingClock(instant bool) *recordingClock {
	return &recordingClock{Fake: clock.NewFake(time.Now()), instant: instant}
}

func (c *recordingClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.waits = append(c.waits, d)
	c.mu.Unlock()
	if !c.instant {
		return c.Fake.After(d)
	}
	c.Advance(d)
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

// Waits returns the waits asked so far.
func (c *recordingClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// waitsOf returns the waits asked of a manager made by newTestManager.
func waitsOf(m *Manager) []time.Duration {
	rc, ok := m.clock.(*recordingClock)
	if !ok {
		panic("waitsOf: the manager does not run on a recordingClock")
	}
	return rc.Waits()
}

// jitterBounds returns the range jitter(d) falls in.
func jitterBounds(d time.Duration) (lo, hi time.Duration) {
	return time.Duration(float64(d) * (1 - backoffJitter)), time.Duration(float64(d) * (1 + backoffJitter))
}
