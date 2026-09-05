package channels

import "errors"

var (
	// ErrNotRunning indicates the channel is not running.
	// Manager will not retry.
	ErrNotRunning = errors.New("channel not running")

	// ErrRateLimit indicates the platform returned a rate-limit response (e.g. HTTP 429).
	// Manager will wait a fixed delay and retry.
	ErrRateLimit = errors.New("rate limited")

	// ErrTemporary indicates a transient failure (e.g. network timeout, 5xx).
	// Manager will use exponential backoff and retry.
	ErrTemporary = errors.New("temporary failure")

	// ErrSendFailed indicates a permanent failure (e.g. invalid chat ID, 4xx non-429).
	// Manager will not retry.
	ErrSendFailed = errors.New("send failed")

	// ErrReceiveOnly indicates the send was refused because the target account is
	// in a receive-only mode (e.g. secmsg/Signal stealth). This is an expected
	// operator choice, not a fault: Manager will not retry and logs it at INFO.
	ErrReceiveOnly = errors.New("recipient account is receive-only")
)
