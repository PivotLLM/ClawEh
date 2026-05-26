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

func TestCooldowns_ClearPerEntry_CallsClearWithSplit(t *testing.T) {
	var (
		gotProvider string
		gotModel    string
	)
	rt := &Runtime{
		ClearCooldown: func(provider, model string) bool {
			gotProvider = provider
			gotModel = model
			return true
		},
	}
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)

	var reply string
	ex.Execute(context.Background(), Request{
		Channel: "cli",
		Text:    "/cooldowns clear openai/gpt-4o",
		Reply: func(text string) error {
			reply = text
			return nil
		},
	})
	if gotProvider != "openai" {
		t.Errorf("provider = %q, want openai", gotProvider)
	}
	if gotModel != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", gotModel)
	}
	if !strings.Contains(reply, "Cleared cooldown for openai/gpt-4o") {
		t.Errorf("missing per-entry confirmation:\n%s", reply)
	}
}

func TestCooldowns_ClearPerEntry_FirstSlashSplit(t *testing.T) {
	// Model names may contain additional slashes; only the FIRST slash separates
	// provider from model.
	var (
		gotProvider string
		gotModel    string
	)
	rt := &Runtime{
		ClearCooldown: func(provider, model string) bool {
			gotProvider = provider
			gotModel = model
			return true
		},
	}
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)

	ex.Execute(context.Background(), Request{
		Channel: "cli",
		Text:    "/cooldowns clear openrouter/meta-llama/llama-4-scout",
		Reply:   func(string) error { return nil },
	})
	if gotProvider != "openrouter" {
		t.Errorf("provider = %q, want openrouter", gotProvider)
	}
	if gotModel != "meta-llama/llama-4-scout" {
		t.Errorf("model = %q, want meta-llama/llama-4-scout", gotModel)
	}
}

func TestCooldowns_ClearPerEntry_NoEntryReportsInformational(t *testing.T) {
	rt := &Runtime{
		ClearCooldown: func(provider, model string) bool { return false },
	}
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)

	var reply string
	ex.Execute(context.Background(), Request{
		Channel: "cli",
		Text:    "/cooldowns clear openai/gpt-4o",
		Reply: func(text string) error {
			reply = text
			return nil
		},
	})
	if !strings.Contains(reply, "No cooldown found for openai/gpt-4o") {
		t.Errorf("missing informational message:\n%s", reply)
	}
}

func TestCooldowns_ClearPerEntry_MalformedRejected(t *testing.T) {
	rt := &Runtime{
		ClearCooldown: func(string, string) bool {
			t.Fatal("ClearCooldown should not be invoked on malformed argument")
			return false
		},
		ResetCooldown: func() {
			t.Fatal("ResetCooldown should not be invoked on malformed argument")
		},
	}
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)

	var reply string
	ex.Execute(context.Background(), Request{
		Channel: "cli",
		Text:    "/cooldowns clear noslash",
		Reply: func(text string) error {
			reply = text
			return nil
		},
	})
	if !strings.Contains(reply, "Usage:") {
		t.Errorf("missing usage hint:\n%s", reply)
	}
}
