package providers

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func TestCreateProvider_ClaudeCli(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "claude-cli", Protocol: "claude-cli"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "claude-sonnet-4.6", Model: "claude-sonnet-4.6", Provider: "claude-cli", Workspace: "/test/ws", Enabled: true},
	}
	cfg.Agents.Defaults.SetDefaultModel("claude-sonnet-4.6")

	provider, _, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider(claude-cli) error = %v", err)
	}

	cliProvider, ok := provider.(*ClaudeCliProvider)
	if !ok {
		t.Fatalf("CreateProvider(claude-cli) returned %T, want *ClaudeCliProvider", provider)
	}
	if cliProvider.Workspace() != "/test/ws" {
		t.Errorf("workspace = %q, want %q", cliProvider.Workspace(), "/test/ws")
	}
}
func TestCreateProvider_ClaudeCliDefaultWorkspace(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "claude-cli", Protocol: "claude-cli"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "claude-cli", Model: "claude-sonnet", Provider: "claude-cli", Enabled: true},
	}
	cfg.Agents.Defaults.SetDefaultModel("claude-cli")
	cfg.Agents.BaseDir = ""

	provider, _, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider error = %v", err)
	}

	cliProvider, ok := provider.(*ClaudeCliProvider)
	if !ok {
		t.Fatalf("returned %T, want *ClaudeCliProvider", provider)
	}
	if cliProvider.Workspace() != "." {
		t.Errorf("workspace = %q, want %q (default)", cliProvider.Workspace(), ".")
	}
}
func TestCreateProvider_CursorCli(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "cursor-cli", Protocol: "cursor-cli"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "cursor", Model: "cursor-cli", Provider: "cursor-cli", Workspace: "/test/ws", Enabled: true},
	}
	cfg.Agents.Defaults.SetDefaultModel("cursor")

	provider, _, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider(cursor-cli) error = %v", err)
	}

	cliProvider, ok := provider.(*CursorCliProvider)
	if !ok {
		t.Fatalf("CreateProvider(cursor-cli) returned %T, want *CursorCliProvider", provider)
	}
	if cliProvider.Workspace() != "/test/ws" {
		t.Errorf("workspace = %q, want %q", cliProvider.Workspace(), "/test/ws")
	}
}

func TestCreateProvider_GeminiCli(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "gemini-cli", Protocol: "gemini-cli"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "gemini-cli", Model: "gemini-cli", Provider: "gemini-cli", Workspace: "/test/ws", Enabled: true},
	}
	cfg.Agents.Defaults.SetDefaultModel("gemini-cli")

	provider, modelID, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider(gemini-cli) error = %v", err)
	}

	geminiProvider, ok := provider.(*GeminiCliProvider)
	if !ok {
		t.Fatalf("CreateProvider(gemini-cli) returned %T, want *GeminiCliProvider", provider)
	}
	if geminiProvider.Workspace() != "/test/ws" {
		t.Errorf("workspace = %q, want %q", geminiProvider.Workspace(), "/test/ws")
	}
	// modelID should be the part after the slash
	if modelID != "gemini-cli" {
		t.Errorf("modelID = %q, want %q", modelID, "gemini-cli")
	}
}
func TestCreateProvider_GeminiCliWithModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "gemini-cli", Protocol: "gemini-cli"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "gemini-flash", Model: "gemini-2.5-flash", Provider: "gemini-cli", Workspace: "/ws", Enabled: true},
	}
	cfg.Agents.Defaults.SetDefaultModel("gemini-flash")

	provider, modelID, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider(gemini-cli/gemini-2.5-flash) error = %v", err)
	}
	if _, ok := provider.(*GeminiCliProvider); !ok {
		t.Fatalf("CreateProvider returned %T, want *GeminiCliProvider", provider)
	}
	// modelID should carry through the actual model name
	if modelID != "gemini-2.5-flash" {
		t.Errorf("modelID = %q, want %q", modelID, "gemini-2.5-flash")
	}
}
func TestCreateProvider_GeminiCliDefaultWorkspace(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.BaseDir = "" // clear base dir so the "." fallback is exercised
	cfg.Providers = []config.Provider{{Name: "gemini-cli", Protocol: "gemini-cli"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "gemini-cli", Model: "gemini-cli", Provider: "gemini-cli", Enabled: true},
	}
	cfg.Agents.Defaults.SetDefaultModel("gemini-cli")

	provider, _, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider error = %v", err)
	}
	geminiProvider, ok := provider.(*GeminiCliProvider)
	if !ok {
		t.Fatalf("returned %T, want *GeminiCliProvider", provider)
	}
	if geminiProvider.Workspace() != "." {
		t.Errorf("workspace = %q, want %q (default)", geminiProvider.Workspace(), ".")
	}
}
