package channels

import "time"

// SetConnAlertAfter shortens c's connection-down alert threshold for tests in
// package channels_test.
func SetConnAlertAfter(c *BaseChannel, d time.Duration) {
	c.conn.mu.Lock()
	c.conn.alertAfter = d
	c.conn.mu.Unlock()
}
