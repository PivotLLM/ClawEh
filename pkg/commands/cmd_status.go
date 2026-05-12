package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

func statusCommand() Definition {
	return Definition{
		Name:        "status",
		Description: "Show runtime status and context size",
		Usage:       "/status",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			var b strings.Builder

			// Header: ClawEh <version>
			fmt.Fprintf(&b, "%s %s\n", global.AppName, global.Version)

			// Uptime (omit if source nil)
			if rt != nil && rt.Uptime != nil {
				d := rt.Uptime().Truncate(time.Second)
				fmt.Fprintf(&b, "Uptime:           %s\n", d.String())
			}

			// Agent name
			if rt != nil && rt.AgentName != "" {
				fmt.Fprintf(&b, "Agent:            %s\n", rt.AgentName)
			}

			// Model (provider)
			if rt != nil && rt.GetModelInfo != nil {
				name, provider := rt.GetModelInfo()
				fmt.Fprintf(&b, "Model:            %s  (%s)\n", name, provider)
			}

			// Channel from request
			if req.Channel != "" {
				fmt.Fprintf(&b, "Channel:          %s\n", req.Channel)
			}

			// Session stats
			if rt != nil && rt.GetSessionStats != nil {
				msgCount, estTokens, summaryChars := rt.GetSessionStats()
				fmt.Fprintf(&b, "Session messages: %d\n", msgCount)
				fmt.Fprintf(&b, "Context tokens:   ~%d      (estimated)\n", estTokens)
				fmt.Fprintf(&b, "Summary chars:    %d\n", summaryChars)
			}

			// Enabled channels
			if rt != nil && rt.GetEnabledChannels != nil {
				names := rt.GetEnabledChannels()
				fmt.Fprintf(&b, "Enabled channels: %d (%s)\n", len(names), strings.Join(names, ", "))
			}

			// Trim trailing newline for cleaner reply.
			out := strings.TrimRight(b.String(), "\n")
			return req.Reply(out)
		},
	}
}
