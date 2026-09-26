package channels

import "time"

// Tuning for channel connections. Every channel keeps retrying its connection
// on its own; these values decide how patiently it retries and when an operator
// is told that retrying is not working.
const (
	// ConnDownAlertAfter is how long a channel may go without a working
	// connection to its service before a "Channel connection down" alert is
	// raised. Retries continue after the alert; it is raised once per outage.
	ConnDownAlertAfter = 10 * time.Minute

	// RetryAfterPadding is added to a server's own retry-after delay (a
	// Telegram 429 "retry after 5" waits 6s), so a retry never lands at the
	// edge of the window the server asked for.
	RetryAfterPadding = time.Second

	// ConnRetryMin and ConnRetryMax bound the backoff between connection
	// attempts when the server gives no retry-after: the wait starts at
	// ConnRetryMin, doubles on each consecutive failure up to ConnRetryMax, and
	// resets once the connection works again.
	ConnRetryMin = 2 * time.Second
	ConnRetryMax = time.Minute
)
