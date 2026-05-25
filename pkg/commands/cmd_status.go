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
//	ClawEh <version>
//
//	```
//	field: value
//	field: value
//	...
//	```
//
// The title is plain text; the body is wrapped in a fenced code block so that
// every channel renderer (Slack mrkdwn, Telegram markdown, webui ReactMarkdown)
// preserves the per-line breaks. Without the fence, webui's Markdown renderer
// collapses single \n into spaces and the body renders as one long blob.
func buildStatusReply(req Request, rt *Runtime) string {
	var body strings.Builder

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
		fmt.Fprintf(&body, "Context tokens: ~%d (estimated)\n", estTokens)
		fmt.Fprintf(&body, "Summary chars: %d\n", summaryChars)
	}
	if rt != nil && rt.GetEnabledChannels != nil {
		names := rt.GetEnabledChannels()
		fmt.Fprintf(&body, "Enabled channels: %d (%s)\n", len(names), strings.Join(names, ", "))
	}

	bodyText := strings.TrimRight(body.String(), "\n")

	var out strings.Builder
	fmt.Fprintf(&out, "%s %s\n", global.AppName, global.Version)
	if bodyText != "" {
		out.WriteString("\n```\n")
		out.WriteString(bodyText)
		out.WriteString("\n```")
	}
	return out.String()
}
