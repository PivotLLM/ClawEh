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

	// StartRetryMin and StartRetryMax bound the backoff between attempts to
	// start a channel whose Start failed (bad credentials, port in use, service
	// unreachable): the wait starts at StartRetryMin, doubles on each failure
	// up to StartRetryMax, and the manager keeps retrying at that ceiling until
	// the channel starts or the gateway stops.
	StartRetryMin = 5 * time.Second
	StartRetryMax = 5 * time.Minute

	// StartRetryAlertAfter is the failed retry on which a "Channel failed to
	// start" alert is raised. Retries continue after the alert; it is raised
	// once per channel start.
	StartRetryAlertAfter = 10
)

// Tuning for the device gateway's authentication surface, which is reachable
// by anything that can open a TCP connection to its listener.
const (
	// DeviceAuthFailThreshold failed authentications from one client IP inside
	// DeviceAuthFailWindow lock that IP out: further connects are answered
	// AUTH_RATE_LIMITED without checking their credentials.
	DeviceAuthFailThreshold = 5
	DeviceAuthFailWindow    = 10 * time.Minute

	// DeviceAuthLockoutMin is the first lockout; each successive lockout for
	// the same IP doubles it, up to DeviceAuthLockoutMax. An IP that has no
	// failure and no lockout in force for DeviceAuthFailWindow is forgotten.
	DeviceAuthLockoutMin = time.Minute
	DeviceAuthLockoutMax = time.Hour

	// DeviceMaxPreauthConns caps connections that are upgraded but not yet
	// authenticated (each holds a goroutine for up to the handshake timeout);
	// further upgrades are refused with 503 until one completes or times out.
	DeviceMaxPreauthConns = 32
)
