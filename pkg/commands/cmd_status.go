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
			return req.Reply(buildStatusReply(req, rt))
		},
	}
}

// buildStatusReply renders the /status output in a channel-agnostic shape:
//
//	```
//	ClawEh Status
//
//	Version: <version>
//	field: value
//	...
//	```
//
// The entire block is wrapped in a fenced code block so that every channel
// renderer (Slack mrkdwn, Telegram markdown, webui ReactMarkdown) preserves
// the per-line breaks. Without the fence, webui's Markdown renderer collapses
// single \n into spaces and the body renders as one long blob.
func buildStatusReply(req Request, rt *Runtime) string {
	var body strings.Builder

	fmt.Fprintf(&body, "Version: %s\n", global.Version)

	if rt != nil && rt.Uptime != nil {
		d := rt.Uptime().Truncate(time.Second)
		fmt.Fprintf(&body, "Uptime: %s\n", d.String())
	}
	if rt != nil && rt.AgentName != "" {
		fmt.Fprintf(&body, "Agent: %s\n", rt.AgentName)
	}
	if rt != nil && rt.GetModelInfo != nil {
		name, provider, apiBase := rt.GetModelInfo()
		fmt.Fprintf(&body, "Model: %s\n", name)
		fmt.Fprintf(&body, "Provider: %s\n", provider)
		if apiBase != "" {
			fmt.Fprintf(&body, "API: %s\n", apiBase)
		}
	}
	if req.Channel != "" {
		fmt.Fprintf(&body, "Channel: %s\n", req.Channel)
	}
	if rt != nil && rt.GetSessionStats != nil {
		msgCount, estTokens, summaryChars := rt.GetSessionStats()
		fmt.Fprintf(&body, "Session messages: %d\n", msgCount)
		if rt.GetArchiveStats != nil {
			archCount, first, last := rt.GetArchiveStats()
			fmt.Fprintf(&body, "Archive messages: %d\n", archCount)
			if !first.IsZero() {
				fmt.Fprintf(&body, "Archive first: %s\n", first.UTC().Format(time.RFC3339))
			}
			if !last.IsZero() {
				fmt.Fprintf(&body, "Archive last: %s\n", last.UTC().Format(time.RFC3339))
			}
		}
		fmt.Fprintf(&body, "Context tokens: ~%d (estimated)\n", estTokens)
		fmt.Fprintf(&body, "Summary chars: %d\n", summaryChars)
	}
	if rt != nil && rt.GetSessionChannels != nil {
		names := rt.GetSessionChannels()
		if len(names) == 0 && req.Channel != "" {
			names = []string{req.Channel}
		}
		if len(names) > 0 {
			fmt.Fprintf(&body, "Agent channels: %d (%s)\n", len(names), strings.Join(names, ", "))
		}
	}

	// Blocked providers (process-wide): only emitted when the runtime exposes
	// a snapshot. Label is explicit so the reader understands the scope —
	// cooldowns are tracked per-process, not per-session.
	if rt != nil && rt.ListCooldowns != nil {
		entries := rt.ListCooldowns()
		body.WriteString("\n")
		if len(entries) == 0 {
			body.WriteString("Blocked providers (process-wide): none\n")
		} else {
			body.WriteString("Blocked providers (process-wide):\n")
			now := time.Now()
			for _, e := range entries {
				fmt.Fprintf(&body, "  %s\n", formatCooldownLine(e, now))
			}
		}
	}

	bodyText := strings.TrimRight(body.String(), "\n")

	var out strings.Builder
	out.WriteString("```\n")
	fmt.Fprintf(&out, "%s Status\n", global.AppName)
	out.WriteString("\n")
	out.WriteString(bodyText)
	out.WriteString("\n```")
	return out.String()
}
