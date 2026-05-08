package commands

import (
	"context"
	"fmt"
)

func cancelCommand() Definition {
	return Definition{
		Name:        "cancel",
		Description: "Skip any messages queued behind the current query",
		Usage:       "/cancel",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.CancelPending == nil {
				return req.Reply(unavailableMsg)
			}
			skipped := rt.CancelPending()
			if skipped == 0 {
				return req.Reply("No pending messages to cancel.")
			}
			return req.Reply(fmt.Sprintf("Cancelled %d pending message(s).", skipped))
		},
	}
}
