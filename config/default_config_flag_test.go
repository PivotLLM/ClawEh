package config

import (
	"path/filepath"
	"slices"
	"testing"
)

// The default_config marker lets the setup wizard tell a fresh, never-saved
// install from a configured one: DefaultConfig() sets it, SaveConfig clears it,
// and SeedDefaultConfig preserves it — across a write/reload round trip.

func TestDefaultConfig_SetsDefaultMarker(t *testing.T) {
	if !DefaultConfig().DefaultConfig {
		t.Fatal("DefaultConfig() must set DefaultConfig=true")
	}
}

func TestSaveConfig_ClearsDefaultMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if cfg.DefaultConfig {
		t.Error("SaveConfig must clear the in-memory marker")
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.DefaultConfig {
		t.Error("a saved config must load with DefaultConfig=false")
	}
}

func TestDefaultConfig_DefaultAgentInheritsDefaultTools(t *testing.T) {
	// The seeded default agent must NOT grant all tools (["*"]); leaving Tools nil
	// makes it inherit the install default tool set, so it never exceeds defaults.
	cfg := DefaultConfig()
	if len(cfg.Agents.List) == 0 {
		t.Fatal("DefaultConfig() should seed a default agent")
	}
	if tools := cfg.Agents.List[0].Tools; len(tools) != 0 {
		t.Errorf("seeded default agent should leave Tools unset (inherit defaults), got %v", tools)
	}
}

func TestDefaultConfig_SeedsAntigravityNotGemini(t *testing.T) {
	// Google deprecated the Gemini CLI in favour of Antigravity (binary "agy"),
	// so the seeded model and provider are Antigravity. The old GEMINI_CLI_*
	// workspace-trust env var goes with it.
	cfg := DefaultConfig()

	var model *ModelConfig
	for i := range cfg.Models {
		switch cfg.Models[i].ModelName {
		case "Antigravity CLI":
			model = &cfg.Models[i]
		case "Gemini CLI":
			t.Errorf("a Gemini CLI model is still seeded: %+v", cfg.Models[i])
		}
	}
	if model == nil {
		t.Fatal("seeded Antigravity CLI model not found")
	}
	if model.Provider != "Antigravity CLI" {
		t.Errorf("provider = %q, want %q", model.Provider, "Antigravity CLI")
	}
	// Headless operation needs approval bypass, exactly as the other CLI models do.
	if !slices.Contains(model.ExtraArgs, "--dangerously-skip-permissions") {
		t.Errorf("extra_args = %v, want --dangerously-skip-permissions", model.ExtraArgs)
	}
	// -p / --print must never be seeded: with either, agy reads the prompt from
	// argv and ignores stdin, silently dropping the conversation.
	for _, a := range model.ExtraArgs {
		if a == "-p" || a == "--print" || a == "--prompt" {
			t.Errorf("extra_args contains %q, which makes agy ignore stdin", a)
		}
	}

	var proto string
	for _, p := range cfg.Providers {
		if p.Name == "Antigravity CLI" {
			proto = p.Protocol
		}
		if p.Protocol == "gemini-cli" {
			t.Errorf("a gemini-cli provider is still seeded: %+v", p)
		}
	}
	if proto != "antigravity-cli" {
		t.Errorf("Antigravity provider protocol = %q, want antigravity-cli", proto)
	}
}

func TestSeedDefaultConfig_PreservesMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	if err := SeedDefaultConfig(path, DefaultConfig()); err != nil {
		t.Fatalf("SeedDefaultConfig: %v", err)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !loaded.DefaultConfig {
		t.Error("a seeded config must load with DefaultConfig=true")
	}
}

// Memory file attachments are budgeted independently of the routed block's
// character cap: a referenced document is injected whole, so a zero here would
// silently disable attachments.
func TestDefaultConfig_MemoryAttachmentBudgets(t *testing.T) {
	p := DefaultConfig().Agents.Defaults.Memory.Prompt
	if p.FileMaxBytes != 256*1024 {
		t.Fatalf("FileMaxBytes = %d, want %d", p.FileMaxBytes, 256*1024)
	}
	if p.FileTotalMaxBytes != 512*1024 {
		t.Fatalf("FileTotalMaxBytes = %d, want %d", p.FileTotalMaxBytes, 512*1024)
	}
	if p.FileTotalMaxBytes < p.FileMaxBytes {
		t.Fatal("per-turn budget must be at least one full attachment")
	}
}
