package commands

import (
	"context"
	"fmt"
	"strings"
)

// quotaCommand surfaces the per-token rate-limit status of an agent's named
// message-API (Integration) tokens so the operator can see which tokens are
// throttled or blocked and clear a block without touching the WebUI. A bare
// /quota (or "/quota list") lists status; "/quota reset [name]" clears blocks.
// It is a simple command (not sub-command routed) so a bare /quota lists rather
// than printing usage.
func quotaCommand() Definition {
	return Definition{
		Name:        "quota",
		Description: "Show message-token rate limits, or clear a block with reset",
		Usage:       "/quota [list | reset [<name>]]",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			switch nthToken(req.Text, 1) { // tokens: [/quota, <sub>, ...]
			case "reset":
				return quotaReset(req, rt)
			case "", "list":
				return req.Reply(renderTokenQuota(rt))
			default:
				return req.Reply("Usage: /quota [list | reset [<name>]]")
			}
		},
	}
}

func quotaReset(req Request, rt *Runtime) error {
	if rt == nil || rt.ResetTokenQuota == nil {
		return req.Reply(unavailableMsg)
	}
	name := nthToken(req.Text, 2) // tokens: [/quota, reset, <name>]
	cleared := rt.ResetTokenQuota(name)
	if name != "" {
		if cleared == 0 {
			return req.Reply(fmt.Sprintf("No active block for token %q.", name))
		}
		return req.Reply(fmt.Sprintf("Cleared block for token %q.", name))
	}
	if cleared == 0 {
		return req.Reply("No active token blocks.")
	}
	return req.Reply(fmt.Sprintf("Cleared %d token block(s).", cleared))
}

func renderTokenQuota(rt *Runtime) string {
	if rt == nil || rt.ListTokenQuota == nil {
		return unavailableMsg
	}
	entries := rt.ListTokenQuota()
	var out strings.Builder
	out.WriteString("```\n")
	out.WriteString("Message-token rate limits:\n")
	if len(entries) == 0 {
		out.WriteString("  none\n")
		out.WriteString("```")
		return out.String()
	}
	for _, e := range entries {
		out.WriteString("  " + formatTokenQuotaLine(e) + "\n")
	}
	out.WriteString("```")
	return out.String()
}

// formatTokenQuotaLine renders one token as
//
//	gps — 30/min — blocked · clears in 14m
//	alarm — 30/min — 3/30 this minute
//
// A blocked token shows its remaining time; an active one shows its current
// window usage against the limit.
func formatTokenQuotaLine(e TokenQuotaEntry) string {
	name := e.Name
	if name == "" {
		name = "(unnamed)"
	}
	head := fmt.Sprintf("%s — %d/min", name, e.RatePerMin)
	if e.Blocked {
		return head + " — blocked · clears in " + formatShortDur(e.BlockRemaining)
	}
	return head + fmt.Sprintf(" — %d/%d this minute", e.HitsInWindow, e.RatePerMin)
}
