package channels

import (
	"errors"
	"time"
)

// Every failed send says why, as one of these sentinels (wrapped with the
// detail), so a caller can test the reason with errors.Is. The Manager decides
// on them whether to retry, how loudly to log and whether to alert: only a
// channel that is not running and a send that failed after its retries raise
// the "Channel send failed" alert; an unknown channel or a recipient that is
// offline or does not exist is a state of the configuration or the recipient,
// not an outage.
var (
	// ErrUnknownChannel indicates no channel of that name is configured.
	// Manager will not retry.
	ErrUnknownChannel = errors.New("unknown channel")

	// ErrNotRunning indicates the channel is not running (stopped, or down).
	// Manager will not retry.
	ErrNotRunning = errors.New("channel not running")

	// ErrRecipientOffline indicates the recipient exists but cannot take a
	// message now (e.g. a paired device that is not connected). Manager will
	// not retry and logs it at WARN.
	ErrRecipientOffline = errors.New("recipient offline")

	// ErrRecipientNotFound indicates the recipient does not exist or cannot
	// be reached through this channel at all (e.g. an unknown chat, a user who
	// blocked the bot, an unpaired device). Manager will not retry and logs it
	// at WARN.
	ErrRecipientNotFound = errors.New("recipient not found")

	// ErrRateLimit indicates the platform returned a rate-limit response (e.g. HTTP 429).
	// Manager retries after a fixed delay, or after the wait the platform
	// asked for when the error carries one (WithRetryAfter).
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

// RetryAfterError is a failed send that carries the wait the platform asked
// for before the next attempt (Telegram's retry_after). It wraps the error,
// so errors.Is still finds its sentinel; read it with errors.As.
type RetryAfterError struct {
	Err   error
	After time.Duration
}

func (e *RetryAfterError) Error() string { return e.Err.Error() }
func (e *RetryAfterError) Unwrap() error { return e.Err }

// WithRetryAfter returns err carrying the wait after; a non-positive after
// returns err unchanged.
func WithRetryAfter(err error, after time.Duration) error {
	if err == nil || after <= 0 {
		return err
	}
	return &RetryAfterError{Err: err, After: after}
}
