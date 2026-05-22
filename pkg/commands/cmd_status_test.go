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

	// Header includes app name and version
	wantHeader := global.AppName + " " + global.Version
	if !strings.Contains(reply, wantHeader) {
		t.Errorf("reply missing header %q\n%s", wantHeader, reply)
	}
	// Uptime — Truncate to seconds, value as "2h13m0s"
	if !strings.Contains(reply, "Uptime:") {
		t.Errorf("reply missing Uptime line\n%s", reply)
	}
	if !strings.Contains(reply, "2h13m0s") {
		t.Errorf("reply missing expected uptime value 2h13m0s\n%s", reply)
	}
	// Agent
	if !strings.Contains(reply, "Agent:") || !strings.Contains(reply, "Alice") {
		t.Errorf("reply missing Agent line or name\n%s", reply)
	}
	// Model
	if !strings.Contains(reply, "Model:") || !strings.Contains(reply, "gpt-4") || !strings.Contains(reply, "openai") {
		t.Errorf("reply missing Model line or values\n%s", reply)
	}
	// Channel
	if !strings.Contains(reply, "Channel:") || !strings.Contains(reply, "telegram") {
		t.Errorf("reply missing Channel line\n%s", reply)
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
		t.Errorf("missing session messages 42\n%s", reply)
	}
	if !strings.Contains(reply, "~9876") {
		t.Errorf("missing estimated tokens ~9876\n%s", reply)
	}
	if !strings.Contains(reply, "Summary chars:    1500") {
		t.Errorf("missing summary chars 1500\n%s", reply)
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
	if !strings.Contains(reply, "Channel:") || !strings.Contains(reply, "cli") {
		t.Errorf("reply missing channel line\n%s", reply)
	}
	// Absent sources must not appear.
	for _, missing := range []string{"Uptime:", "Agent:", "Model:", "Session messages:", "Context tokens:", "Summary chars:", "Enabled channels:"} {
		if strings.Contains(reply, missing) {
			t.Errorf("reply unexpectedly contains %q when source nil\n%s", missing, reply)
		}
	}
}
