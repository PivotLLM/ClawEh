package commands

import (
	"context"
	"fmt"
)

func cancelCommand() Definition {
	return Definition{
		Name:        "cancel",
		Description: "Stop the current request and skip any messages queued behind it",
		Usage:       "/cancel",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.CancelPending == nil {
				return req.Reply(unavailableMsg)
			}
			return req.Reply(cancelReply(rt.CancelPending()))
		},
	}
}

// cancelReply words the /cancel outcome: a stopped turn, dropped queued
// messages, both, or nothing to do.
func cancelReply(running bool, skipped int) string {
	switch {
	case running && skipped > 0:
		return fmt.Sprintf("Cancelled the current request and %d pending message(s).", skipped)
	case running:
		return "Cancelled the current request."
	case skipped > 0:
		return fmt.Sprintf("Cancelled %d pending message(s).", skipped)
	default:
		return "No pending messages to cancel."
	}
}
