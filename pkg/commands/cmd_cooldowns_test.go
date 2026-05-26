package commands

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCooldowns_ListNone(t *testing.T) {
	rt := &Runtime{
		ListCooldowns: func() []CooldownEntry { return nil },
	}
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)

	var reply string
	ex.Execute(context.Background(), Request{
		Channel: "cli",
		Text:    "/cooldowns list",
		Reply: func(text string) error {
			reply = text
			return nil
		},
	})
	if !strings.Contains(reply, "Blocked providers (process-wide):") {
		t.Errorf("missing header:\n%s", reply)
	}
	if !strings.Contains(reply, "none") {
		t.Errorf("missing 'none':\n%s", reply)
	}
}

func TestCooldowns_ListEntries(t *testing.T) {
	now := time.Now()
	rt := &Runtime{
		ListCooldowns: func() []CooldownEntry {
			return []CooldownEntry{
				{
					Provider: "openai",
					Model:    "gpt-4",
					Reason:   "billing",
					Since:    now.Add(-2 * time.Minute),
					Until:    now.Add(3 * time.Minute),
				},
				{
					Provider: "anthropic",
					Model:    "claude",
					Reason:   "rate_limit",
					Since:    now.Add(-30 * time.Second),
					Until:    now.Add(30 * time.Second),
				},
			}
		},
	}
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)

	var reply string
	ex.Execute(context.Background(), Request{
		Channel: "cli",
		Text:    "/cooldowns list",
		Reply: func(text string) error {
			reply = text
			return nil
		},
	})

	if !strings.Contains(reply, "openai/gpt-4 — billing") {
		t.Errorf("missing openai billing line:\n%s", reply)
	}
	if !strings.Contains(reply, "anthropic/claude — rate_limit") {
		t.Errorf("missing anthropic line:\n%s", reply)
	}
	if !strings.Contains(reply, "since ") || !strings.Contains(reply, "until ") {
		t.Errorf("missing since/until annotations:\n%s", reply)
	}
}

func TestCooldowns_ClearCallsResetCooldown(t *testing.T) {
	called := false
	rt := &Runtime{
		ResetCooldown: func() { called = true },
	}
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)

	var reply string
	ex.Execute(context.Background(), Request{
		Channel: "cli",
		Text:    "/cooldowns clear",
		Reply: func(text string) error {
			reply = text
			return nil
		},
	})
	if !called {
		t.Error("ResetCooldown was not invoked")
	}
	if !strings.Contains(reply, "Cleared") {
		t.Errorf("missing confirmation:\n%s", reply)
	}
}
