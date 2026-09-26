package channels

import (
	"fmt"
	"sync"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/logger"
)

// connWatch tracks one channel's connection outage. The zero value is ready:
// no outage in progress.
type connWatch struct {
	mu sync.Mutex
	// alertAfter overrides ConnDownAlertAfter when non-zero (tests only).
	alertAfter time.Duration
	downSince  time.Time // zero while the connection works
	lastErr    string
	timer      *time.Timer
	alerted    bool
}

func (w *connWatch) resetLocked() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.downSince = time.Time{}
	w.lastErr = ""
	w.alerted = false
}

// ReportConnected records that the channel's connection to its service works,
// ending any outage in progress. Channels call it on every successful poll,
// connect or reconnect; it is cheap when nothing is wrong.
func (c *BaseChannel) ReportConnected() {
	w := &c.conn
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.downSince.IsZero() {
		return
	}
	if w.alerted {
		logger.InfoCF("channels", "Channel connection restored", map[string]any{
			"channel":  c.name,
			"down_for": time.Since(w.downSince).Round(time.Second).String(),
		})
	}
	w.resetLocked()
}

// ReportConnFailure records a failed connection attempt that the channel is
// retrying. The first failure of an outage starts the clock; if no
// ReportConnected arrives within ConnDownAlertAfter, one "Channel connection
// down" alert is raised carrying the latest error. Faults that no retry can fix
// (a revoked token) are alerted by the channel itself, at once.
func (c *BaseChannel) ReportConnFailure(err error) {
	w := &c.conn
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.lastErr = err.Error()
	}
	if !w.downSince.IsZero() {
		return
	}
	after := w.alertAfter
	if after <= 0 {
		after = ConnDownAlertAfter
	}
	start := time.Now()
	w.downSince = start
	w.timer = time.AfterFunc(after, func() { c.connDownExpired(start, after) })
}

// ConnDownSince returns when the current connection outage began, or the zero
// time while the connection works.
func (c *BaseChannel) ConnDownSince() time.Time {
	c.conn.mu.Lock()
	defer c.conn.mu.Unlock()
	return c.conn.downSince
}

// connDownExpired runs when an outage that began at start has lasted after.
func (c *BaseChannel) connDownExpired(start time.Time, after time.Duration) {
	w := &c.conn
	w.mu.Lock()
	if !w.downSince.Equal(start) || w.alerted {
		w.mu.Unlock()
		return
	}
	if !c.IsRunning() {
		// Stopped mid-outage: nothing is retrying, so there is nothing to
		// report. A later Start begins a fresh outage.
		w.resetLocked()
		w.mu.Unlock()
		return
	}
	w.alerted = true
	w.timer = nil
	lastErr := w.lastErr
	w.mu.Unlock()

	c.Alert(alerter.Alert{
		Title:       "Channel connection down",
		Description: fmt.Sprintf("%s: no working connection for %s; the channel keeps retrying", c.name, after),
		Details:     lastErr,
	})
}

// NextConnRetry returns the wait before the next connection attempt after a
// wait of cur: ConnRetryMin when cur is zero, otherwise double cur, capped at
// ConnRetryMax.
func NextConnRetry(cur time.Duration) time.Duration {
	if cur <= 0 {
		return ConnRetryMin
	}
	return min(cur*2, ConnRetryMax)
}
