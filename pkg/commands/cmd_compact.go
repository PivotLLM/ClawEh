package commands

import (
	"context"
	"errors"

	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
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
			report, err := rt.CompactHistory(ctx)
			// The report describes the full outcome (per-model attempts + a
			// success/failure/nothing final line), so prefer it verbatim.
			if report != "" {
				return req.Reply(report)
			}
			if err != nil {
				if errors.Is(err, llmcontext.ErrNothingToCompress) {
					return req.Reply("Already compact — nothing to summarize.")
				}
				return req.Reply("Failed to compact history: " + err.Error())
			}
			return req.Reply("Conversation history compacted.")
		},
	}
}
