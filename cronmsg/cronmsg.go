// ClawEh
// License: MIT

// Package cronmsg is the single source of truth for the cron-message wrapper
// format. The scheduler produces a wrapped message announcing the fire time so
// the LLM knows the message originated from a scheduled job. Every fire carries
// its own fire time, so no two fires of a job are identical and none is ever
// collapsed as a repeat: each fire is a request in its own right.
//
// The wrapped form is:
//
//	[<fp>] The following message is from a cron job that fired at <ts>:\n\n<message>
//
// where "[<fp>] " is an optional leading lowercase-hex fingerprint identifying
// the job. When the fingerprint is empty the un-marked form is produced.
package cronmsg

import (
	"fmt"
	"time"
)

// prefix is the cron-wrapper header that precedes the fire timestamp. It is the
// single definition of this literal.
const prefix = "The following message is from a cron job that fired at "

// timeFormat is the layout used for the embedded fire timestamp. It must match
// the format historically produced by the scheduler so that already-persisted
// messages continue to round-trip.
const timeFormat = "2006-01-02 15:04 MST"

// Build returns message wrapped with the cron metadata header indicating the
// fire time so the LLM knows the message originated from a scheduled job. When
// fingerprint is non-empty it is emitted FIRST as a leading "[<fp>] " marker
// identifying the job. When fingerprint is empty the un-marked form is produced.
func Build(fingerprint string, fireTime time.Time, message string) string {
	ts := fireTime.Format(timeFormat)
	body := fmt.Sprintf("%s%s:\n\n%s", prefix, ts, message)
	if fingerprint == "" {
		return body
	}
	return fmt.Sprintf("[%s] %s", fingerprint, body)
}

// eventPrefix is the header of a message delivered by a continuous monitor (a
// listen job). It is deliberately a different literal from prefix, so an event
// never reads as a cron fire.
const eventPrefix = "The following event was received by a continuous monitor at "

// eventTimeFormat carries seconds, unlike the cron fire time. The store drops
// a user message identical to the previous one as noise; a listener may
// deliver the same event twice in a row on purpose, and a listener never
// delivers more than once every two seconds, so second granularity keeps two
// identical events distinct.
const eventTimeFormat = "2006-01-02 15:04:05 MST"

// BuildEvent wraps an event delivered by a listen job: the operator's note,
// then the source tool's full result introduced by name. With an empty result
// (an operational notice from the monitor itself) only the note is included.
// The output is not a cron-wrapper message; see eventPrefix.
func BuildEvent(at time.Time, message, source, result string) string {
	body := fmt.Sprintf("%s%s:\n\n%s", eventPrefix, at.Format(eventTimeFormat), message)
	if result == "" {
		return body
	}
	return fmt.Sprintf("%s\n\n%s returned the following:\n%s", body, source, result)
}
