package channels

import "github.com/PivotLLM/ClawEh/internal/clock"

// SetConnClock sets the clock c's connection-down alert is timed on, for
// tests in package channels_test.
func SetConnClock(c *BaseChannel, clk clock.Clock) {
	c.conn.mu.Lock()
	c.conn.clock = clk
	c.conn.mu.Unlock()
}
