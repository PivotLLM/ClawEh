package channels

import (
	"time"

	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/internal/clock"
)

// SetConnClock sets the clock c's connection-down alert is timed on, for
// tests in package channels_test.
func SetConnClock(c *BaseChannel, clk clock.Clock) {
	c.conn.mu.Lock()
	c.conn.clock = clk
	c.conn.mu.Unlock()
}

// MaxSendRetries is how often a send is retried, for tests in package
// channels_test.
const MaxSendRetries = maxRetries

// NewTestManagerWith returns a manager that sends to ch, registered as name,
// with an unlimited rate and retries that wait on an instant clock, for tests
// in package channels_test.
func NewTestManagerWith(name string, ch Channel) *Manager {
	m := newTestManager()
	m.channels[name] = ch
	m.workers[name] = &channelWorker{ch: ch, limiter: rate.NewLimiter(rate.Inf, 1)}
	return m
}

// ManagerWaits returns the retry waits a manager made by NewTestManagerWith
// has asked for.
func ManagerWaits(m *Manager) []time.Duration { return waitsOf(m) }
