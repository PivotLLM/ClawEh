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
		GetModelInfo: func() (string, string, string) {
			return "gpt-4", "openai", ""
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
		GetModelInfo: func() (string, string, string) {
			return "claude-opus-4-7", "anthropic", ""
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
	for _, missing := range []string{"Uptime:", "Agent:", "Model:", "Session messages:", "Context tokens:", "Summary chars:", "Enabled channels:"} {
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
		GetModelInfo: func() (string, string, string) {
			return "DeepSeek-V4-Flash", "openai", ""
		},
		Uptime: func() time.Duration {
			return 59*time.Minute + 38*time.Second
		},
		GetSessionStats: func() (int, int, int) {
			return 4, 148, 0
		},
		GetEnabledChannels: func() []string {
			return []string{
				"telegram-Amber", "telegram-Dawn", "telegram-Karen",
				"telegram-Penny", "telegram-Wendy", "slack", "webui",
			}
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
		"Provider: openai",
		"Channel: webui",
		"Session messages: 4",
		"Context tokens: ~148 (estimated)",
		"Summary chars: 0",
		"Enabled channels: 7 (telegram-Amber, telegram-Dawn, telegram-Karen, telegram-Penny, telegram-Wendy, slack, webui)",
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
