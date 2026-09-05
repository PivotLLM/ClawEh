package commands

import (
	"context"
)

func memoryCommand() Definition {
	return Definition{
		Name:        "memory",
		Description: "Show cognitive-memory status (domains, pending, last consolidation)",
		Usage:       "/memory",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.GetMemoryStatus == nil {
				return req.Reply("Cognitive memory is not available.")
			}
			status := rt.GetMemoryStatus()
			if status == "" {
				return req.Reply("Cognitive memory is not enabled for this agent.")
			}
			// Fenced so every channel renderer preserves the per-line breaks.
			return req.Reply("```\n" + status + "\n```")
		},
	}
}
