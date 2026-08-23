// ClawEh
// License: MIT

package timetool

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fixedTool returns a NowTool pinned to one instant so assertions never race
// the wall clock.
func fixedTool(t time.Time) *NowTool {
	return &NowTool{now: func() time.Time { return t }}
}

// TestNow_ReportsEveryFieldAModelNeeds covers the whole rendered payload: a
// human-readable form for prose, the zone and offset for conversions, RFC3339
// for arithmetic, and the epoch.
func TestNow_ReportsEveryFieldAModelNeeds(t *testing.T) {
	utc := time.Date(2026, 2, 12, 15, 4, 5, 0, time.UTC)
	res := fixedTool(utc).Execute(context.Background(), nil)

	if res.IsError {
		t.Fatalf("unexpected error: %s", res.ForLLM)
	}
	for _, want := range []string{
		"Thursday, February 12, 2026 at 3:04 PM",
		"timezone: UTC (UTC+00:00)",
		"rfc3339: 2026-02-12T15:04:05Z",
		"unix: 1770908645",
	} {
		if !strings.Contains(res.ForLLM, want) {
			t.Errorf("missing %q in:\n%s", want, res.ForLLM)
		}
	}
}

// TestNow_ConvertsToRequestedTimezone is the reason the tool takes an argument
// at all — the date anchor in the system prompt is server-local and cannot
// answer "what time is it for them".
func TestNow_ConvertsToRequestedTimezone(t *testing.T) {
	utc := time.Date(2026, 2, 12, 15, 4, 5, 0, time.UTC)
	res := fixedTool(utc).Execute(context.Background(), map[string]any{"timezone": "Asia/Kolkata"})

	if res.IsError {
		t.Fatalf("unexpected error: %s", res.ForLLM)
	}
	// Kolkata is UTC+05:30 year-round, so this is stable regardless of DST rules.
	if !strings.Contains(res.ForLLM, "UTC+05:30") {
		t.Errorf("expected a +05:30 offset, got:\n%s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "8:34 PM") {
		t.Errorf("expected the converted local time, got:\n%s", res.ForLLM)
	}
}

// TestNow_BadTimezoneStillReportsTime keeps a typo from stalling the turn: the
// error names the mistake and carries the local time anyway.
func TestNow_BadTimezoneStillReportsTime(t *testing.T) {
	utc := time.Date(2026, 2, 12, 15, 4, 5, 0, time.UTC)
	res := fixedTool(utc).Execute(context.Background(), map[string]any{"timezone": "Mars/Olympus"})

	if !res.IsError {
		t.Error("an unknown timezone should be reported as an error")
	}
	if !strings.Contains(res.ForLLM, "Mars/Olympus") {
		t.Errorf("error should name the rejected zone, got: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "2026") {
		t.Errorf("error should still carry the time, got: %s", res.ForLLM)
	}
}

// TestNow_BlankTimezoneIsLocal covers the empty-string case, which arrives from
// models that fill every declared argument whether or not they mean to.
func TestNow_BlankTimezoneIsLocal(t *testing.T) {
	utc := time.Date(2026, 2, 12, 15, 4, 5, 0, time.UTC)
	res := fixedTool(utc).Execute(context.Background(), map[string]any{"timezone": "   "})
	if res.IsError {
		t.Fatalf("a blank timezone should fall back to local, got error: %s", res.ForLLM)
	}
}

// TestFormatOffset covers the sign and the half-hour zones, where a naive
// hours-only formatter silently loses 30 minutes.
func TestFormatOffset(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    string
	}{
		{0, "+00:00"},
		{5 * 3600, "+05:00"},
		{5*3600 + 30*60, "+05:30"},
		{-4 * 3600, "-04:00"},
		{-(3*3600 + 30*60), "-03:30"}, // Newfoundland
	} {
		if got := formatOffset(tc.seconds); got != tc.want {
			t.Errorf("formatOffset(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

// TestNow_SchemaHasNoRequiredArgs guards the calling contract: the tool must be
// callable with no arguments, since that is how it will nearly always be used.
func TestNow_SchemaHasNoRequiredArgs(t *testing.T) {
	params := NewNowTool().Parameters()
	required, _ := params["required"].([]string)
	if len(required) != 0 {
		t.Errorf("time_now must be callable with no arguments, required = %v", required)
	}
}
