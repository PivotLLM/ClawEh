package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// setupTestEnv creates an isolated HOME/CLAW_HOME with a minimal valid config
// (one openai api-key provider + model + default agent) and returns the config
// path plus a cleanup func. Shared by the API handler tests.
func setupTestEnv(t *testing.T) (string, func()) {
	t.Helper()

	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldClawHome := os.Getenv("CLAW_HOME")

	if err := os.Setenv("HOME", tmp); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	if err := os.Setenv("CLAW_HOME", filepath.Join(tmp, ".claw")); err != nil {
		t.Fatalf("set CLAW_HOME: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{
		Name:     "openai",
		Protocol: "openai-chat",
		BaseURL:  "https://api.openai.com/v1",
		APIKey:   "sk-default",
	}}
	cfg.Models = []config.ModelConfig{{
		ModelName: "custom-default",
		Model:     "gpt-4o",
		Provider:  "openai",
		Enabled:   true,
	}}
	cfg.Agents.Defaults.SetDefaultModel("custom-default")
	cfg.Agents.List = []config.AgentConfig{{
		ID:      "main",
		Name:    "Main",
		Default: true,
	}}

	configPath := filepath.Join(tmp, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}

	cleanup := func() {
		_ = os.Setenv("HOME", oldHome)
		if oldClawHome == "" {
			_ = os.Unsetenv("CLAW_HOME")
		} else {
			_ = os.Setenv("CLAW_HOME", oldClawHome)
		}
	}
	return configPath, cleanup
}
