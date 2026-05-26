package commands

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// cooldownsCommand exposes the process-wide cooldown tracker so the operator
// can list which providers are blocked and clear them without also resending
// the last message (which is what /retry does). Useful after a billing top-up.
func cooldownsCommand() Definition {
	return Definition{
		Name:        "cooldowns",
		Description: "List or clear provider cooldowns (process-wide)",
		SubCommands: []SubCommand{
			{
				Name:        "list",
				Description: "Show blocked providers",
				Handler: func(_ context.Context, req Request, rt *Runtime) error {
					return req.Reply(renderCooldownList(rt))
				},
			},
			{
				Name:        "clear",
				Description: "Clear all provider cooldowns",
				Handler: func(_ context.Context, req Request, rt *Runtime) error {
					if rt == nil || rt.ResetCooldown == nil {
						return req.Reply(unavailableMsg)
					}
					rt.ResetCooldown()
					return req.Reply("Cleared all provider cooldowns.")
				},
			},
		},
	}
}

func renderCooldownList(rt *Runtime) string {
	if rt == nil || rt.ListCooldowns == nil {
		return unavailableMsg
	}
	entries := rt.ListCooldowns()
	var out strings.Builder
	out.WriteString("```\n")
	out.WriteString("Blocked providers (process-wide):\n")
	if len(entries) == 0 {
		out.WriteString("  none\n")
		out.WriteString("```")
		return out.String()
	}
	now := time.Now()
	for _, e := range entries {
		out.WriteString("  " + formatCooldownLine(e, now) + "\n")
	}
	out.WriteString("```")
	return out.String()
}

// formatCooldownLine renders a single CooldownEntry as
//
//	openai/gpt-4 — billing — since 12:34:56 (Xm ago); until 12:39:56 (Xm)
//
// Times are rendered in the local clock zone — operators read /status on the
// host they run claw on, not in UTC.
func formatCooldownLine(e CooldownEntry, now time.Time) string {
	reason := e.Reason
	if reason == "" {
		reason = "unknown"
	}
	parts := []string{fmt.Sprintf("%s/%s — %s", e.Provider, e.Model, reason)}
	if !e.Since.IsZero() {
		ago := now.Sub(e.Since).Round(time.Second)
		parts = append(parts, fmt.Sprintf("since %s (%s ago)", e.Since.Format("15:04:05"), formatShortDur(ago)))
	}
	if !e.Until.IsZero() {
		remaining := e.Until.Sub(now).Round(time.Second)
		if remaining < 0 {
			remaining = 0
		}
		parts = append(parts, fmt.Sprintf("until %s (%s)", e.Until.Format("15:04:05"), formatShortDur(remaining)))
	}
	return strings.Join(parts, " — ")
}

func formatShortDur(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
