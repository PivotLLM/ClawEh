package providers

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
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

func TestCreateProvider_AntigravityCli(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "antigravity-cli", Protocol: "antigravity-cli"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "antigravity-cli", Model: "antigravity-cli", Provider: "antigravity-cli", Workspace: "/test/ws", Enabled: true},
	}
	cfg.Agents.Defaults.SetDefaultModel("antigravity-cli")

	provider, modelID, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider(antigravity-cli) error = %v", err)
	}

	agyProvider, ok := provider.(*AntigravityCliProvider)
	if !ok {
		t.Fatalf("CreateProvider(antigravity-cli) returned %T, want *AntigravityCliProvider", provider)
	}
	if agyProvider.Workspace() != "/test/ws" {
		t.Errorf("workspace = %q, want %q", agyProvider.Workspace(), "/test/ws")
	}
	// modelID should be the part after the slash
	if modelID != "antigravity-cli" {
		t.Errorf("modelID = %q, want %q", modelID, "antigravity-cli")
	}
}

func TestCreateProvider_AntigravityCliWithModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "antigravity-cli", Protocol: "antigravity-cli"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "gemini-flash", Model: "gemini-2.5-flash", Provider: "antigravity-cli", Workspace: "/ws", Enabled: true},
	}
	cfg.Agents.Defaults.SetDefaultModel("gemini-flash")

	provider, modelID, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider(antigravity-cli/gemini-2.5-flash) error = %v", err)
	}
	if _, ok := provider.(*AntigravityCliProvider); !ok {
		t.Fatalf("CreateProvider returned %T, want *AntigravityCliProvider", provider)
	}
	// modelID should carry through the actual model name
	if modelID != "gemini-2.5-flash" {
		t.Errorf("modelID = %q, want %q", modelID, "gemini-2.5-flash")
	}
}

func TestCreateProvider_AntigravityCliDefaultWorkspace(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.BaseDir = "" // clear base dir so the "." fallback is exercised
	cfg.Providers = []config.Provider{{Name: "antigravity-cli", Protocol: "antigravity-cli"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "antigravity-cli", Model: "antigravity-cli", Provider: "antigravity-cli", Enabled: true},
	}
	cfg.Agents.Defaults.SetDefaultModel("antigravity-cli")

	provider, _, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider error = %v", err)
	}
	agyProvider, ok := provider.(*AntigravityCliProvider)
	if !ok {
		t.Fatalf("returned %T, want *AntigravityCliProvider", provider)
	}
	if agyProvider.Workspace() != "." {
		t.Errorf("workspace = %q, want %q (default)", agyProvider.Workspace(), ".")
	}
}

// An existing config naming the deprecated gemini-cli protocol keeps starting,
// and runs agy.
//
// Google deprecated the Gemini CLI, so the protocol is an alias rather than a
// second provider. Without it an upgraded install would fail at startup with
// "unknown protocol" — the config is released, and operators have it written
// down.
func TestCreateProvider_GeminiCliIsAnAliasForAntigravity(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "Gemini CLI", Protocol: "gemini-cli"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "legacy", Model: "gemini-2.5-pro", Provider: "Gemini CLI", Workspace: "/ws", Enabled: true},
	}
	cfg.Agents.Defaults.SetDefaultModel("legacy")

	provider, modelID, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("a config naming gemini-cli no longer starts: %v", err)
	}
	if _, ok := provider.(*AntigravityCliProvider); !ok {
		t.Fatalf("gemini-cli produced %T, want *AntigravityCliProvider", provider)
	}
	if modelID != "gemini-2.5-pro" {
		t.Errorf("modelID = %q, want the configured model to carry through", modelID)
	}
}

// The MCP host must still auto-start for a config naming gemini-cli. Missing
// this would not fail loudly: the CLI would run and simply have no claw tools.
func TestGeminiCliStillCountsAsACLIProvider(t *testing.T) {
	for _, proto := range []string{"antigravity-cli", "gemini-cli"} {
		if !config.IsCLIProtocol(proto) {
			t.Errorf("IsCLIProtocol(%q) = false; the MCP host would not auto-start", proto)
		}
	}
}
