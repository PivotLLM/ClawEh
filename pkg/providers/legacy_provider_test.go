package providers

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// Tests for CreateProvider (legacy_provider.go).

func TestCreateProvider_NoModelList_NoProviders_ReturnsError(t *testing.T) {
	cfg := config.DefaultConfig()
	// Clear everything so there's no model_list and no providers config.
	cfg.ModelList = nil
	cfg.Agents.Defaults.SetDefaultModel("")
	// Ensure no providers are configured so HasProvidersConfig returns false.
	cfg.Providers = config.ProvidersConfig{}

	_, _, err := CreateProvider(cfg)
	if err == nil {
		t.Fatal("expected error when no providers and no model_list configured")
	}
}

func TestCreateProvider_WithModelList_ValidModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []config.ModelConfig{
		{
			ModelName: "my-groq",
			Model:     "groq/llama-3.3-70b",
			APIKey:    "gsk-test",
			Enabled:   true,
		},
	}
	cfg.Agents.Defaults.SetDefaultModel("my-groq")

	provider, modelID, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
	if modelID != "llama-3.3-70b" {
		t.Errorf("modelID = %q, want llama-3.3-70b", modelID)
	}
}

func TestCreateProvider_ModelNotFoundInList_ReturnsError(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []config.ModelConfig{
		{
			ModelName: "my-groq",
			Model:     "groq/llama-3.3-70b",
			APIKey:    "gsk-test",
			Enabled:   true,
		},
	}
	cfg.Agents.Defaults.SetDefaultModel("nonexistent-model")

	_, _, err := CreateProvider(cfg)
	if err == nil {
		t.Fatal("expected error for model not found in model_list")
	}
	if !strings.Contains(err.Error(), "not found in model_list") {
		t.Errorf("error = %q, want 'not found in model_list'", err.Error())
	}
}

func TestCreateProvider_WithProvidersConfig_NoMatchingModel(t *testing.T) {
	cfg := config.DefaultConfig()
	// Start with an empty model_list.
	cfg.ModelList = nil
	// Configure a provider via the legacy providers config.
	cfg.Providers.Groq.APIKey = "gsk-test"
	// Set a model name that doesn't match any migration output name.
	cfg.Agents.Defaults.SetDefaultModel("nonexistent-model-xyz")

	// This exercises the HasProvidersConfig() merge path, then fails on model lookup.
	_, _, err := CreateProvider(cfg)
	if err == nil {
		t.Fatal("expected error when model not found after providers merge")
	}
}

func TestCreateProvider_WorkspaceInjected(t *testing.T) {
	cfg := config.DefaultConfig()
	dir := t.TempDir()
	cfg.Agents.Defaults.Workspace = dir
	cfg.ModelList = []config.ModelConfig{
		{
			ModelName: "my-claude-cli",
			Model:     "claude-cli/claude-sonnet-4.6",
			Enabled:   true,
		},
	}
	cfg.Agents.Defaults.SetDefaultModel("my-claude-cli")

	provider, _, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestCreateProvider_TimeoutInjected(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.RequestTimeout = 60
	cfg.ModelList = []config.ModelConfig{
		{
			ModelName: "my-openai",
			Model:     "openai/gpt-4o",
			APIKey:    "sk-test",
			Enabled:   true,
		},
	}
	cfg.Agents.Defaults.SetDefaultModel("my-openai")

	provider, _, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
}
