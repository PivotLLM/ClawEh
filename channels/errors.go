package channels

import "errors"

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
