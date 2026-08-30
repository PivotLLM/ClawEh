// ClawEh
// License: MIT

// Package timetool provides the current date and time to the LLM on demand.
//
// The system prompt carries only a DATE, deliberately: it sits in the cached
// prefix, and every HTTP provider ClawEh dispatches to caches by
// longest-common-prefix, so a value that changes every minute would invalidate
// the cache for the entire conversation history behind it. A date changes once
// a day; the time is available here instead, for the turns that actually need it.
//
// The date anchor is not redundant with this tool. A model with no date in
// context does not ask for one — it answers from its training cutoff, silently
// and confidently. The anchor prevents that; this tool supplies the precision
// the anchor gives up.
package timetool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// NowTool reports the current date, time, and timezone.
type NowTool struct {
	// now is the clock, injectable for tests. nil means time.Now.
	now func() time.Time
}

// NewNowTool builds the tool with the system clock.
func NewNowTool() *NowTool { return &NowTool{} }

func (t *NowTool) Name() string { return "now" }

func (t *NowTool) Description() string {
	return "Get the current date, time, and timezone. The system prompt carries today's date only, " +
		"so call this whenever you need the time of day, the timezone or UTC offset, or a precise " +
		"timestamp — for example to work out how long until something, or to convert between zones."
}

func (t *NowTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"timezone": map[string]any{
				"type": "string",
				"description": "Optional IANA timezone name (e.g. \"America/Toronto\", \"UTC\"). " +
					"Defaults to the server's local timezone.",
			},
		},
		"required": []string{},
	}
}

func (t *NowTool) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

func (t *NowTool) Execute(_ context.Context, args map[string]any) *tools.ToolResult {
	now := t.clock()

	if tz, _ := args["timezone"].(string); strings.TrimSpace(tz) != "" {
		loc, err := time.LoadLocation(strings.TrimSpace(tz))
		if err != nil {
			// Naming an unknown zone is a recoverable mistake: report it and give
			// the local time anyway, so the turn does not stall on a typo.
			return tools.ErrorResult(fmt.Sprintf(
				"unknown timezone %q — use an IANA name such as \"America/Toronto\" or \"UTC\". Local time is %s",
				tz, render(now)))
		}
		now = now.In(loc)
	}

	return tools.SilentResult(render(now))
}

// render formats one instant with everything a model might need from it: a
// human-readable form, the IANA-style zone abbreviation and offset, RFC3339 for
// arithmetic, and the Unix epoch.
func render(t time.Time) string {
	zone, offset := t.Zone()
	return fmt.Sprintf(
		"%s\ntimezone: %s (UTC%s)\nrfc3339: %s\nunix: %d",
		t.Format("Monday, January 2, 2006 at 3:04 PM"),
		zone,
		formatOffset(offset),
		t.Format(time.RFC3339),
		t.Unix(),
	)
}

// formatOffset renders a zone offset in seconds as "+05:30" / "-04:00" / "+00:00".
func formatOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return fmt.Sprintf("%s%02d:%02d", sign, seconds/3600, (seconds%3600)/60)
}
