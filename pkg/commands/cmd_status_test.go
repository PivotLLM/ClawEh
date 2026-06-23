package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

func TestStatus_BasicFields(t *testing.T) {
	rt := &Runtime{
		AgentName: "Alice",
		GetModelInfo: func() (string, string, string, string) {
			return "gpt-4", "openai", "openai", ""
		},
		Uptime: func() time.Duration {
			return 2*time.Hour + 13*time.Minute
		},
		GetSessionStats: func() (int, int, int) {
			return 7, 1234, 256
		},
		GetEnabledChannels: func() []string {
			return []string{"telegram", "discord"}
		},
		GetSessionChannels: func() []string {
			return []string{"telegram"}
		},
	}
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)

	var reply string
	res := ex.Execute(context.Background(), Request{
		Channel: "telegram",
		Text:    "/status",
		Reply: func(text string) error {
			reply = text
			return nil
		},
	})
	if res.Outcome != OutcomeHandled {
		t.Fatalf("outcome=%v, want=%v", res.Outcome, OutcomeHandled)
	}

	// Header is "<AppName> Status"
	wantHeader := global.AppName + " Status"
	if !strings.Contains(reply, wantHeader) {
		t.Errorf("reply missing header %q\n%s", wantHeader, reply)
	}
	// Version appears on its own labelled line
	if !strings.Contains(reply, "Version: "+global.Version) {
		t.Errorf("reply missing 'Version: %s'\n%s", global.Version, reply)
	}
	// Uptime — Truncate to seconds, value as "2h13m0s"
	if !strings.Contains(reply, "Uptime: 2h13m0s") {
		t.Errorf("reply missing 'Uptime: 2h13m0s'\n%s", reply)
	}
	// Agent
	if !strings.Contains(reply, "Agent: Alice") {
		t.Errorf("reply missing 'Agent: Alice'\n%s", reply)
	}
	// Model
	if !strings.Contains(reply, "Model: gpt-4") {
		t.Errorf("reply missing 'Model: gpt-4'\n%s", reply)
	}
	if !strings.Contains(reply, "Provider: openai") {
		t.Errorf("reply missing 'Provider: openai'\n%s", reply)
	}
	// Channel
	if !strings.Contains(reply, "Channel: telegram") {
		t.Errorf("reply missing 'Channel: telegram'\n%s", reply)
	}
}

func TestStatus_SessionStatsReflected(t *testing.T) {
	rt := &Runtime{
		AgentName: "Bob",
		GetModelInfo: func() (string, string, string, string) {
			return "claude-opus-4-7", "anthropic", "anthropic", ""
		},
		Uptime: func() time.Duration { return 5 * time.Second },
		GetSessionStats: func() (int, int, int) {
			return 42, 9876, 1500
		},
	}
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)

	var reply string
	res := ex.Execute(context.Background(), Request{
		Channel: "cli",
		Text:    "/status",
		Reply: func(text string) error {
			reply = text
			return nil
		},
	})
	if res.Outcome != OutcomeHandled {
		t.Fatalf("outcome=%v, want=%v", res.Outcome, OutcomeHandled)
	}

	if !strings.Contains(reply, "Session messages: 42") {
		t.Errorf("missing 'Session messages: 42'\n%s", reply)
	}
	if !strings.Contains(reply, "Context tokens: ~9876 (estimated)") {
		t.Errorf("missing 'Context tokens: ~9876 (estimated)'\n%s", reply)
	}
	if !strings.Contains(reply, "Summary chars: 1500") {
		t.Errorf("missing 'Summary chars: 1500'\n%s", reply)
	}
}

func TestStatus_ContextWindowReflected(t *testing.T) {
	// With GetContextWindow set, /status shows the window size and the usage
	// percentage (50000 of 150000 ≈ 33%).
	rt := &Runtime{
		AgentName:        "Bob",
		Uptime:           func() time.Duration { return time.Second },
		GetSessionStats:  func() (int, int, int) { return 10, 50000, 0 },
		GetContextWindow: func() int { return 150000 },
	}
	reply := buildStatusReply(Request{Channel: "cli"}, rt)
	if !strings.Contains(reply, "Context window: 150000 tokens") {
		t.Errorf("missing 'Context window: 150000 tokens'\n%s", reply)
	}
	if !strings.Contains(reply, "Context tokens: ~50000 (estimated, 33%)") {
		t.Errorf("missing usage with percentage\n%s", reply)
	}
}

func TestStatus_ContextWindowUnknownFallsBack(t *testing.T) {
	// GetContextWindow nil → the plain usage line, no window/percentage.
	rt := &Runtime{
		GetSessionStats: func() (int, int, int) { return 1, 1234, 0 },
	}
	reply := buildStatusReply(Request{Channel: "cli"}, rt)
	if !strings.Contains(reply, "Context tokens: ~1234 (estimated)") {
		t.Errorf("missing plain usage fallback\n%s", reply)
	}
	if strings.Contains(reply, "Context window:") {
		t.Errorf("unexpected window line when GetContextWindow is nil\n%s", reply)
	}
}

func TestStatus_GracefulDegradation(t *testing.T) {
	// All optional sources nil — handler must still produce a usable reply.
	rt := &Runtime{}
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)

	var reply string
	res := ex.Execute(context.Background(), Request{
		Channel: "cli",
		Text:    "/status",
		Reply: func(text string) error {
			reply = text
			return nil
		},
	})
	if res.Outcome != OutcomeHandled {
		t.Fatalf("outcome=%v, want=%v", res.Outcome, OutcomeHandled)
	}
	if reply == "" {
		t.Fatal("expected non-empty reply for graceful degradation")
	}
	// Header must still be present.
	if !strings.Contains(reply, global.AppName) {
		t.Errorf("reply missing app name when sources nil\n%s", reply)
	}
	// Channel comes from req.Channel directly, not from rt.
	if !strings.Contains(reply, "Channel: cli") {
		t.Errorf("reply missing 'Channel: cli'\n%s", reply)
	}
	// Absent sources must not appear.
	for _, missing := range []string{"Uptime:", "Agent:", "Model:", "Session messages:", "Context tokens:", "Summary chars:", "Enabled channels:", "Agent channels:", "Archive messages:"} {
		if strings.Contains(reply, missing) {
			t.Errorf("reply unexpectedly contains %q when source nil\n%s", missing, reply)
		}
	}
}

// TestStatus_ExactShape verifies the rendered shape end-to-end:
//   - line 1: opening code fence "```"
//   - line 2: "<AppName> Status"
//   - line 3: blank
//   - line 4: "Version: <version>"
//   - subsequent lines: "field: value" with NO leading whitespace and NO
//     internal padding between the colon and the value
//   - final line: closing code fence "```"
//
// The fence wrapping guarantees newlines survive every channel renderer
// (notably webui's Markdown renderer, which would otherwise collapse single
// newlines into spaces).
func TestStatus_ExactShape(t *testing.T) {
	rt := &Runtime{
		AgentName: "Amber",
		GetModelInfo: func() (string, string, string, string) {
			return "DeepSeek-V4-Flash", "openrouter", "openai", ""
		},
		Uptime: func() time.Duration {
			return 59*time.Minute + 38*time.Second
		},
		GetSessionStats: func() (int, int, int) {
			return 4, 148, 0
		},
		GetEnabledChannels: func() []string {
			// Globally enabled — must NOT appear in /status output.
			return []string{
				"telegram-Amber", "telegram-Dawn", "telegram-Karen",
				"telegram-Penny", "telegram-Wendy", "slack", "webui",
			}
		},
		GetSessionChannels: func() []string {
			// Only the channels Amber is bound to in config.
			return []string{"telegram-Amber"}
		},
		GetArchiveStats: func() (int, time.Time, time.Time) {
			// Count present, timestamps absent → archive date lines omitted.
			return 12, time.Time{}, time.Time{}
		},
	}

	reply := buildStatusReply(Request{Channel: "webui"}, rt)
	lines := strings.Split(reply, "\n")

	if lines[0] != "```" {
		t.Errorf("line 1 = %q, want opening code fence ```", lines[0])
	}
	wantHeader := global.AppName + " Status"
	if lines[1] != wantHeader {
		t.Errorf("line 2 = %q, want %q", lines[1], wantHeader)
	}
	if lines[2] != "" {
		t.Errorf("line 3 = %q, want empty (separator)", lines[2])
	}
	if lines[len(lines)-1] != "```" {
		t.Errorf("last line = %q, want closing code fence ```", lines[len(lines)-1])
	}

	// Exactly one opening and one closing fence in total.
	fenceCount := 0
	for _, l := range lines {
		if l == "```" {
			fenceCount++
		}
	}
	if fenceCount != 2 {
		t.Errorf("got %d fence lines, want exactly 2", fenceCount)
	}

	wantBody := []string{
		"Version: " + global.Version,
		"Uptime: 59m38s",
		"Agent: Amber",
		"Model: DeepSeek-V4-Flash",
		"Provider: openrouter",
		"Protocol: openai",
		"Channel: webui",
		"Session messages: 4",
		"Archive messages: 12",
		"Context tokens: ~148 (estimated)",
		"Summary chars: 0",
		"Agent channels: 1 (telegram-Amber)",
	}
	body := lines[3 : len(lines)-1]
	if len(body) != len(wantBody) {
		t.Fatalf("body has %d lines, want %d:\n%s", len(body), len(wantBody), reply)
	}
	for i, want := range wantBody {
		if body[i] != want {
			t.Errorf("body line %d = %q, want %q", i, body[i], want)
		}
	}

	// No tab characters anywhere.
	if strings.ContainsRune(reply, '\t') {
		t.Errorf("reply contains tab character:\n%s", reply)
	}
	// No leading whitespace on field lines (everything between the fences).
	for i, line := range body {
		if line != strings.TrimLeft(line, " \t") {
			t.Errorf("field line %d has leading whitespace: %q", i, line)
		}
	}
	// No run of 2+ internal spaces (catches old padding/right-alignment style).
	for _, line := range body {
		if strings.Contains(line, "  ") {
			// Allow it only inside parenthesised tails like the enabled-channels list,
			// which we know has no double-space in the test data above.
			t.Errorf("field line contains double space (padding artifact): %q", line)
		}
	}
}

// TestStatus_ChannelScopingDoesNotLeakGlobalChannels verifies that /status
// renders only the channels reachable by the current agent's bindings, and
// does NOT enumerate every channel the daemon has configured. Previously the
// handler called GetEnabledChannels and exposed the full daemon-wide list to
// any caller — a Slack user could see what Telegram channels were configured.
// The fix is to scope the list via GetSessionChannels, which the runtime
// derives from cfg.Bindings filtered by the current agent ID.
func TestStatus_ChannelScopingDoesNotLeakGlobalChannels(t *testing.T) {
	rt := &Runtime{
		AgentName: "SlackOnlyAgent",
		// Global registry — must NOT appear in the reply.
		GetEnabledChannels: func() []string {
			return []string{
				"telegram-Amber", "telegram-Dawn", "telegram-Karen",
				"telegram-Penny", "telegram-Wendy", "slack", "webui",
			}
		},
		// Per-agent scope — single channel.
		GetSessionChannels: func() []string {
			return []string{"slack"}
		},
		GetSessionStats: func() (int, int, int) { return 1, 10, 0 },
		Uptime:          func() time.Duration { return time.Second },
	}

	reply := buildStatusReply(Request{Channel: "slack"}, rt)

	// Old global-only channel names must not appear anywhere in the reply.
	for _, leaked := range []string{
		"telegram-Amber", "telegram-Dawn", "telegram-Karen",
		"telegram-Penny", "telegram-Wendy", "webui",
	} {
		if strings.Contains(reply, leaked) {
			t.Errorf("reply leaked globally-enabled channel %q:\n%s", leaked, reply)
		}
	}
	// The pre-fix label must not be present.
	if strings.Contains(reply, "Enabled channels:") {
		t.Errorf("reply still uses pre-fix 'Enabled channels:' label:\n%s", reply)
	}
	// The scoped, single-channel line must be present.
	if !strings.Contains(reply, "Agent channels: 1 (slack)") {
		t.Errorf("missing scoped 'Agent channels: 1 (slack)':\n%s", reply)
	}
}

// TestStatus_ChannelScopingFallbackToRequestChannel verifies that when the
// agent has no bindings registered (GetSessionChannels returns empty) the
// status reply falls back to listing the channel the request came from —
// the minimum we can prove the agent is reachable on — instead of
// emitting nothing or, worse, the global list.
func TestStatus_ChannelScopingFallbackToRequestChannel(t *testing.T) {
	rt := &Runtime{
		AgentName:          "DefaultAgent",
		GetSessionChannels: func() []string { return nil },
		GetEnabledChannels: func() []string {
			return []string{"telegram-Amber", "slack", "webui"}
		},
		Uptime: func() time.Duration { return time.Second },
	}
	reply := buildStatusReply(Request{Channel: "webui"}, rt)
	if !strings.Contains(reply, "Agent channels: 1 (webui)") {
		t.Errorf("missing fallback 'Agent channels: 1 (webui)':\n%s", reply)
	}
	if strings.Contains(reply, "telegram-Amber") || strings.Contains(reply, "slack") {
		t.Errorf("reply leaked other channels in fallback case:\n%s", reply)
	}
}

// TestStatus_ArchiveCount verifies the new 'Archive messages: <N>' line is
// emitted from GetArchiveStats and sits directly after 'Session messages:'.
// Zero-valued timestamps must suppress the optional 'Archive first/last' lines.
func TestStatus_ArchiveCount(t *testing.T) {
	rt := &Runtime{
		GetSessionStats: func() (int, int, int) { return 3, 100, 0 },
		GetArchiveStats: func() (int, time.Time, time.Time) {
			return 42, time.Time{}, time.Time{}
		},
	}
	reply := buildStatusReply(Request{Channel: "cli"}, rt)
	if !strings.Contains(reply, "Archive messages: 42") {
		t.Errorf("missing 'Archive messages: 42':\n%s", reply)
	}
	// Date lines must be absent when timestamps are zero.
	for _, absent := range []string{"Archive first:", "Archive last:"} {
		if strings.Contains(reply, absent) {
			t.Errorf("unexpected %q for zero timestamps:\n%s", absent, reply)
		}
	}
	// Archive line must appear immediately after Session messages.
	lines := strings.Split(reply, "\n")
	for i, l := range lines {
		if l == "Session messages: 3" {
			if i+1 >= len(lines) || lines[i+1] != "Archive messages: 42" {
				t.Errorf("Archive line not adjacent to Session messages:\n%s", reply)
			}
			return
		}
	}
	t.Errorf("Session messages line not found:\n%s", reply)
}

// TestStatus_ArchiveDateRange verifies that when GetArchiveStats returns
// non-zero first/last timestamps, the optional Archive first/last lines
// are emitted in RFC3339 UTC form.
func TestStatus_ArchiveDateRange(t *testing.T) {
	first := time.Date(2026, 4, 1, 12, 30, 0, 0, time.UTC)
	last := time.Date(2026, 5, 25, 9, 15, 0, 0, time.UTC)
	rt := &Runtime{
		GetSessionStats: func() (int, int, int) { return 2, 50, 0 },
		GetArchiveStats: func() (int, time.Time, time.Time) {
			return 99, first, last
		},
	}
	reply := buildStatusReply(Request{Channel: "cli"}, rt)
	if !strings.Contains(reply, "Archive first: 2026-04-01T12:30:00Z") {
		t.Errorf("missing 'Archive first: 2026-04-01T12:30:00Z':\n%s", reply)
	}
	if !strings.Contains(reply, "Archive last: 2026-05-25T09:15:00Z") {
		t.Errorf("missing 'Archive last: 2026-05-25T09:15:00Z':\n%s", reply)
	}
}

// TestStatus_BlockedProvidersNone verifies the "none" path of the blocked-
// providers section: when the runtime exposes ListCooldowns but the snapshot
// is empty, /status emits a single-line "Blocked providers (process-wide):
// none" entry.
func TestStatus_BlockedProvidersNone(t *testing.T) {
	rt := &Runtime{
		ListCooldowns: func() []CooldownEntry { return nil },
	}
	reply := buildStatusReply(Request{Channel: "cli"}, rt)
	if !strings.Contains(reply, "Blocked providers (process-wide): none") {
		t.Errorf("missing 'Blocked providers (process-wide): none':\n%s", reply)
	}
}

// TestStatus_BlockedProvidersWithEntries verifies the populated path: each
// CooldownEntry renders as one indented line under the section header.
func TestStatus_BlockedProvidersWithEntries(t *testing.T) {
	now := time.Now()
	rt := &Runtime{
		ListCooldowns: func() []CooldownEntry {
			return []CooldownEntry{
				{
					Provider: "openai",
					Model:    "gpt-4",
					Reason:   "billing",
					Since:    now.Add(-90 * time.Second),
					Until:    now.Add(210 * time.Second),
				},
			}
		},
	}
	reply := buildStatusReply(Request{Channel: "cli"}, rt)
	if !strings.Contains(reply, "Blocked providers (process-wide):") {
		t.Errorf("missing section header:\n%s", reply)
	}
	if !strings.Contains(reply, "openai/gpt-4 — billing") {
		t.Errorf("missing entry line:\n%s", reply)
	}
}

// TestStatus_BlockedProvidersAbsentWhenNoCallback verifies the section is
// omitted entirely when the runtime does not expose ListCooldowns — old
// callers must keep their original layout.
func TestStatus_BlockedProvidersAbsentWhenNoCallback(t *testing.T) {
	rt := &Runtime{}
	reply := buildStatusReply(Request{Channel: "cli"}, rt)
	if strings.Contains(reply, "Blocked providers") {
		t.Errorf("unexpected 'Blocked providers' section when ListCooldowns nil:\n%s", reply)
	}
}
