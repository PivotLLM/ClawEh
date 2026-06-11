package providers

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// Tests for CreateProvider (legacy_provider.go).

func TestCreateProvider_NoModelList_ReturnsError(t *testing.T) {
	cfg := config.DefaultConfig()
	// Clear everything so there's no model_list configured.
	cfg.ModelList = nil
	cfg.Agents.Defaults.SetDefaultModel("")

	_, _, err := CreateProvider(cfg)
	if err == nil {
		t.Fatal("expected error when no model_list configured")
	}
}

func TestCreateProvider_WithModelList_ValidModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{
		{Name: "groq", Protocol: "openai", BaseURL: "https://api.groq.com/openai/v1", APIKey: "gsk-test"},
	}
	cfg.ModelList = []config.ModelConfig{
		{
			ModelName: "my-groq",
			Model:     "llama-3.3-70b",
			Provider:  "groq",
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
	cfg.Providers = []config.Provider{
		{Name: "groq", Protocol: "openai", BaseURL: "https://api.groq.com/openai/v1", APIKey: "gsk-test"},
	}
	cfg.ModelList = []config.ModelConfig{
		{
			ModelName: "my-groq",
			Model:     "llama-3.3-70b",
			Provider:  "groq",
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

func TestCreateProvider_UnknownProvider_ReturnsError(t *testing.T) {
	cfg := config.DefaultConfig()
	// Model references a provider that is not configured.
	cfg.Providers = nil
	cfg.ModelList = []config.ModelConfig{
		{
			ModelName: "my-groq",
			Model:     "llama-3.3-70b",
			Provider:  "groq",
			Enabled:   true,
		},
	}
	cfg.Agents.Defaults.SetDefaultModel("my-groq")

	_, _, err := CreateProvider(cfg)
	if err == nil {
		t.Fatal("expected error when model references an unconfigured provider")
	}
}

func TestCreateProvider_WorkspaceInjected(t *testing.T) {
	cfg := config.DefaultConfig()
	dir := t.TempDir()
	cfg.Agents.Defaults.Workspace = dir
	cfg.Providers = []config.Provider{
		{Name: "claude-cli", Protocol: "claude-cli"},
	}
	cfg.ModelList = []config.ModelConfig{
		{
			ModelName: "my-claude-cli",
			Model:     "claude-sonnet-4.6",
			Provider:  "claude-cli",
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
	cfg.Providers = []config.Provider{
		{Name: "openai", Protocol: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"},
	}
	cfg.ModelList = []config.ModelConfig{
		{
			ModelName: "my-openai",
			Model:     "gpt-4o",
			Provider:  "openai",
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
