package commands

import (
	"context"
	"fmt"
)

func clearCommand() Definition {
	return Definition{
		Name:        "clear",
		Aliases:     []string{"reset"},
		Description: "Clear the chat history",
		Usage:       "/clear",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.ClearHistory == nil {
				return req.Reply(unavailableMsg)
			}
			var cancelNote string
			if rt.CancelPending != nil {
				if skipped := rt.CancelPending(); skipped > 0 {
					cancelNote = fmt.Sprintf(" (%d pending message(s) cancelled)", skipped)
				}
			}
			if err := rt.ClearHistory(); err != nil {
				return req.Reply("Failed to clear chat history: " + err.Error())
			}
			return req.Reply("Conversation history cleared. Starting fresh!" + cancelNote)
		},
	}
}
