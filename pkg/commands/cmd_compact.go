package commands

import (
	"context"
)

func compactCommand() Definition {
	return Definition{
		Name:        "compact",
		Description: "Summarize and compress the conversation history",
		Usage:       "/compact",
		Handler: func(ctx context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.CompactHistory == nil {
				return req.Reply(unavailableMsg)
			}
			if err := rt.CompactHistory(ctx); err != nil {
				return req.Reply("Failed to compact history: " + err.Error())
			}
			return req.Reply("Conversation history compacted.")
		},
	}
}
