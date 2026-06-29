package config

import (
	"path/filepath"
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

func TestDefaultConfig_GeminiCLITrustsWorkspace(t *testing.T) {
	// Newer Gemini CLI refuses to run headless in an "untrusted" folder, so the
	// seeded model must set GEMINI_CLI_TRUST_WORKSPACE=true or tool calls fail.
	cfg := DefaultConfig()
	var found bool
	for _, m := range cfg.Models {
		if m.ModelName == "Gemini CLI" {
			found = true
			if m.Env["GEMINI_CLI_TRUST_WORKSPACE"] != "true" {
				t.Errorf("Gemini CLI model env = %v, want GEMINI_CLI_TRUST_WORKSPACE=true", m.Env)
			}
		}
	}
	if !found {
		t.Fatal("seeded Gemini CLI model not found")
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
