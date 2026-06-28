package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

func TestAgentModels_RoundTrip(t *testing.T) {
	var a AgentConfig
	data := `{"id": "x", "models": ["claude-opus", "gpt-4o-mini", "haiku"]}`
	if err := json.Unmarshal([]byte(data), &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(a.Models) != 3 {
		t.Fatalf("Models len = %d, want 3", len(a.Models))
	}
	if a.Models[0] != "claude-opus" || a.Models[1] != "gpt-4o-mini" || a.Models[2] != "haiku" {
		t.Errorf("Models = %v", a.Models)
	}

	out, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	models, ok := result["models"].([]any)
	if !ok || len(models) != 3 || models[0] != "claude-opus" {
		t.Errorf("models = %v", result["models"])
	}
}

func TestAgentConfig_FullParse(t *testing.T) {
	jsonData := `{
		"agents": {
			"defaults": {
				"workspace": "~/.claw/workspace",
				"model": "glm-4.7",
				"max_tokens": 8192,
				"max_tool_iterations": 20
			},
			"list": [
				{
					"id": "sales",
					"default": true,
					"name": "Sales Bot",
					"models": ["gpt-4"]
				},
				{
					"id": "support",
					"name": "Support Bot",
					"models": ["claude-opus", "haiku"],
					"subagents": {
						"allow_agents": ["sales"]
					}
				}
			]
		},
		"bindings": [
			{
				"agent_id": "support",
				"match": {
					"channel": "telegram",
					"account_id": "*",
					"peer": {"kind": "direct", "id": "user123"}
				}
			}
		],
		"session": {
			"mode": "per-user",
			"identity_links": {
				"john": ["telegram:123", "discord:john#1234"]
			}
		}
	}`

	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(jsonData), cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(cfg.Agents.List) != 2 {
		t.Fatalf("agents.list len = %d, want 2", len(cfg.Agents.List))
	}

	sales := cfg.Agents.List[0]
	if sales.ID != "sales" || !sales.Default || sales.Name != "Sales Bot" {
		t.Errorf("sales = %+v", sales)
	}
	if len(sales.Models) != 1 || sales.Models[0] != "gpt-4" {
		t.Errorf("sales.Models = %v", sales.Models)
	}

	support := cfg.Agents.List[1]
	if support.ID != "support" || support.Name != "Support Bot" {
		t.Errorf("support = %+v", support)
	}
	if len(support.Models) != 2 || support.Models[0] != "claude-opus" || support.Models[1] != "haiku" {
		t.Errorf("support.Models = %v", support.Models)
	}
	if support.Subagents == nil || len(support.Subagents.AllowAgents) != 1 {
		t.Errorf("support.Subagents = %+v", support.Subagents)
	}

	if len(cfg.Bindings) != 1 {
		t.Fatalf("bindings len = %d, want 1", len(cfg.Bindings))
	}
	binding := cfg.Bindings[0]
	if binding.AgentID != "support" || binding.Match.Channel != "telegram" {
		t.Errorf("binding = %+v", binding)
	}
	if binding.Match.Peer == nil || binding.Match.Peer.Kind != "direct" || binding.Match.Peer.ID != "user123" {
		t.Errorf("binding.Match.Peer = %+v", binding.Match.Peer)
	}

	if cfg.Session.Mode != "per-user" {
		t.Errorf("Session.Mode = %q", cfg.Session.Mode)
	}
	if len(cfg.Session.IdentityLinks) != 1 {
		t.Errorf("Session.IdentityLinks = %v", cfg.Session.IdentityLinks)
	}
	links := cfg.Session.IdentityLinks["john"]
	if len(links) != 2 {
		t.Errorf("john links = %v", links)
	}
}

func TestConfig_NoAgentsListInheritsDefault(t *testing.T) {
	jsonData := `{
		"agents": {
			"defaults": {
				"workspace": "~/.claw/workspace",
				"model": "glm-4.7",
				"max_tokens": 8192,
				"max_tool_iterations": 20
			}
		}
	}`

	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(jsonData), cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// A config that omits agents.list should still have the default "claw"
	// agent baked in by DefaultConfig(), so the gateway can start cleanly.
	if len(cfg.Agents.List) != 1 || cfg.Agents.List[0].ID != "claw" {
		t.Errorf("expected default claw agent to be preserved, got %+v", cfg.Agents.List)
	}
	if len(cfg.Bindings) != 0 {
		t.Errorf("bindings should be empty, got %d", len(cfg.Bindings))
	}
}

// TestDefaultConfig_WorkspacePath verifies workspace path is correctly set
func TestDefaultConfig_WorkspacePath(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.WorkspacePath() == "" {
		t.Error("Workspace should not be empty")
	}
}

// TestDefaultConfig_Retention verifies the generated config carries the
// long-lived-memory retention day limits while leaving the count caps unlimited.
func TestDefaultConfig_Retention(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.ArchiveDays != 365 {
		t.Errorf("ArchiveDays = %d, want 365", cfg.Agents.Defaults.ArchiveDays)
	}
	if cfg.Agents.Defaults.SummaryRetentionDays != 3650 {
		t.Errorf("SummaryRetentionDays = %d, want 3650", cfg.Agents.Defaults.SummaryRetentionDays)
	}
	if cfg.Agents.Defaults.ArchiveMessageCount != 0 {
		t.Errorf("ArchiveMessageCount = %d, want 0 (unlimited)", cfg.Agents.Defaults.ArchiveMessageCount)
	}
	if cfg.Agents.Defaults.SummaryMaxCount != 0 {
		t.Errorf("SummaryMaxCount = %d, want 0 (unlimited)", cfg.Agents.Defaults.SummaryMaxCount)
	}
}

// TestDefaultConfig_Model verifies the default model list is ordered with
// "Claude CLI" first and "Codex CLI" second.
func TestDefaultConfig_Model(t *testing.T) {
	cfg := DefaultConfig()

	if len(cfg.Agents.Defaults.Models) != 2 {
		t.Fatalf("Models len = %d, want 2", len(cfg.Agents.Defaults.Models))
	}
	if cfg.Agents.Defaults.Models[0] != "Claude CLI" {
		t.Errorf("Models[0] = %q, want Claude CLI", cfg.Agents.Defaults.Models[0])
	}
	if cfg.Agents.Defaults.Models[1] != "Codex CLI" {
		t.Errorf("Models[1] = %q, want Codex CLI", cfg.Agents.Defaults.Models[1])
	}
}

// TestDefaultConfig_MaxTokens verifies max tokens has default value
func TestDefaultConfig_MaxTokens(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.MaxTokens == 0 {
		t.Error("MaxTokens should not be zero")
	}
}

// TestDefaultConfig_MaxToolIterations verifies max tool iterations has default value
func TestDefaultConfig_MaxToolIterations(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.MaxToolIterations == 0 {
		t.Error("MaxToolIterations should not be zero")
	}
}

// TestDefaultConfig_Temperature verifies temperature has default value
func TestDefaultConfig_Temperature(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.Temperature != nil {
		t.Error("Temperature should be nil when not provided")
	}
}

// TestDefaultConfig_Gateway verifies gateway defaults
func TestDefaultConfig_Gateway(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Gateway.Host != "127.0.0.1" {
		t.Error("Gateway host should have default value")
	}
	if cfg.Gateway.Port == 0 {
		t.Error("Gateway port should have default value")
	}
}

// TestDefaultConfig_Providers verifies the seeded providers carry no credentials by default.
func TestDefaultConfig_Providers(t *testing.T) {
	cfg := DefaultConfig()

	for _, name := range []string{"Anthropic", "OpenAI", "OpenRouter Chat"} {
		prov, err := cfg.GetProvider(name)
		if err != nil {
			t.Fatalf("GetProvider(%q): %v", name, err)
		}
		if prov.APIKey != "" {
			t.Errorf("%s API key should be empty by default", name)
		}
	}
}

// TestDefaultConfig_Validates guards the first-run experience: internal.LoadConfig
// writes DefaultConfig() to disk and immediately reloads it through the
// validators, so the default must pass ValidateProviders and ValidateModels
// (every model's provider reference resolves, every provider is well-formed).
func TestDefaultConfig_Validates(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.ValidateProviders(); err != nil {
		t.Fatalf("DefaultConfig ValidateProviders: %v", err)
	}
	if err := cfg.ValidateModels(); err != nil {
		t.Fatalf("DefaultConfig ValidateModels: %v", err)
	}
	// Every model must reference a configured provider.
	for _, m := range cfg.Models {
		if _, err := cfg.GetProvider(m.Provider); err != nil {
			t.Errorf("model %q: %v", m.ModelName, err)
		}
	}
	// The default agent model must exist in the model list.
	def := cfg.Agents.Defaults.DefaultModelName()
	found := false
	for _, m := range cfg.Models {
		if m.ModelName == def {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("default model %q not present in models", def)
	}
}

// TestDefaultConfig_Channels verifies channels are disabled by default
func TestDefaultConfig_Channels(t *testing.T) {
	cfg := DefaultConfig()

	for _, bot := range cfg.Channels.Telegram {
		if bot.Enabled {
			t.Error("Telegram bots should be disabled by default")
		}
	}
	if cfg.Channels.Discord.Enabled {
		t.Error("Discord should be disabled by default")
	}
	if cfg.Channels.Slack.Enabled {
		t.Error("Slack should be disabled by default")
	}
	if cfg.Channels.Matrix.Enabled {
		t.Error("Matrix should be disabled by default")
	}
}

// TestDefaultConfig_WebTools verifies web tools config
func TestDefaultConfig_WebTools(t *testing.T) {
	cfg := DefaultConfig()

	// Verify web tools defaults
	if cfg.Tools.Web.Brave.MaxResults != 5 {
		t.Error("Expected Brave MaxResults 5, got ", cfg.Tools.Web.Brave.MaxResults)
	}
	if len(cfg.Tools.Web.Brave.APIKeys) != 0 {
		t.Error("Brave API key should be empty by default")
	}
	if cfg.Tools.Web.DuckDuckGo.MaxResults != 5 {
		t.Error("Expected DuckDuckGo MaxResults 5, got ", cfg.Tools.Web.DuckDuckGo.MaxResults)
	}
}

func TestSaveConfig_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	cfg := DefaultConfig()
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("config file has permission %04o, want 0600", perm)
	}
}

// TestConfig_Complete verifies all config fields are set
func TestConfig_Complete(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.WorkspacePath() == "" {
		t.Error("Workspace should not be empty")
	}
	if len(cfg.Agents.Defaults.Models) == 0 || cfg.Agents.Defaults.Models[0] == "" {
		t.Error("Model should be set in default config")
	}
	if cfg.Agents.Defaults.Temperature != nil {
		t.Error("Temperature should be nil when not provided")
	}
	if cfg.Agents.Defaults.MaxTokens == 0 {
		t.Error("MaxTokens should not be zero")
	}
	if cfg.Agents.Defaults.MaxToolIterations == 0 {
		t.Error("MaxToolIterations should not be zero")
	}
	if cfg.Gateway.Host != "127.0.0.1" {
		t.Error("Gateway host should have default value")
	}
	if cfg.Gateway.Port == 0 {
		t.Error("Gateway port should have default value")
	}
}

func TestDefaultConfig_ExecAllowRemoteDisabled(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Tools.Exec.AllowRemote {
		t.Fatal("DefaultConfig().Tools.Exec.AllowRemote should be false (exec restricted to internal channels by default)")
	}
}

func TestLoadConfig_ExecAllowRemoteDefaultsFalseWhenUnset(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"tools":{"exec":{"enable_deny_patterns":true}}}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Tools.Exec.AllowRemote {
		t.Fatal("tools.exec.allow_remote should default to false when unset in config file")
	}
}

func TestLoadConfig_ExecAllowRemoteCanBeEnabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"tools":{"exec":{"allow_remote":true}}}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if !cfg.Tools.Exec.AllowRemote {
		t.Fatal("tools.exec.allow_remote should be true when explicitly enabled in config file")
	}
}

func TestLoadConfig_WebToolsProxy(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	configJSON := `{
  "agents": {"defaults":{"workspace":"./workspace","model":"gpt4","max_tokens":8192,"max_tool_iterations":20}},
  "providers": [{"name":"openai","protocol":"openai-chat","base_url":"https://api.openai.com/v1","api_key":"x"}],
  "models": [{"model_name":"gpt4","model":"gpt-5.4","provider":"openai","enabled":true}],
  "tools": {"web":{"proxy":"http://127.0.0.1:7890"}}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Tools.Web.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("Tools.Web.Proxy = %q, want %q", cfg.Tools.Web.Proxy, "http://127.0.0.1:7890")
	}
}

// TestDefaultConfig_SummarizationThresholds verifies compress field defaults
func TestDefaultConfig_SummarizationThresholds(t *testing.T) {
	cfg := DefaultConfig()
	// New compress fields default to 0 (= use llmcontext defaults).
	if cfg.Agents.Defaults.CompressNormalPercent != 0 {
		t.Errorf("CompressNormalPercent = %d, want 0 (use llmcontext default)", cfg.Agents.Defaults.CompressNormalPercent)
	}
	if cfg.Agents.Defaults.CompressMessageThreshold != 0 {
		t.Errorf("CompressMessageThreshold = %d, want 0 (use llmcontext default)", cfg.Agents.Defaults.CompressMessageThreshold)
	}
}

func TestDefaultConfig_SessionMode(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Session.Mode != "unified" {
		t.Errorf("Session.Mode = %q, want 'unified'", cfg.Session.Mode)
	}
}

func TestDefaultConfig_WorkspacePath_Default(t *testing.T) {
	// Unset to ensure we test the default
	t.Setenv("CLAW_HOME", "")
	// Set a known home for consistent test results
	t.Setenv("HOME", "/tmp/home")

	cfg := DefaultConfig()
	want := filepath.Join("/tmp/home", ".claw", "agents", "default")

	if cfg.WorkspacePath() != want {
		t.Errorf("Default workspace path = %q, want %q", cfg.WorkspacePath(), want)
	}
}

func TestDefaultConfig_WorkspacePath_WithClawHome(t *testing.T) {
	t.Setenv("CLAW_HOME", "/custom/claw/home")

	cfg := DefaultConfig()
	want := "/custom/claw/home/agents/default"

	if cfg.WorkspacePath() != want {
		t.Errorf("Workspace path with CLAW_HOME = %q, want %q", cfg.WorkspacePath(), want)
	}
}

// TestFlexibleStringSlice_UnmarshalText tests UnmarshalText with various comma separators
func TestFlexibleStringSlice_UnmarshalText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "English commas only",
			input:    "123,456,789",
			expected: []string{"123", "456", "789"},
		},
		{
			name:     "Chinese commas only",
			input:    "123，456，789",
			expected: []string{"123", "456", "789"},
		},
		{
			name:     "Mixed English and Chinese commas",
			input:    "123,456，789",
			expected: []string{"123", "456", "789"},
		},
		{
			name:     "Single value",
			input:    "123",
			expected: []string{"123"},
		},
		{
			name:     "Values with whitespace",
			input:    " 123 , 456 , 789 ",
			expected: []string{"123", "456", "789"},
		},
		{
			name:     "Empty string",
			input:    "",
			expected: nil,
		},
		{
			name:     "Only commas - English",
			input:    ",,",
			expected: []string{},
		},
		{
			name:     "Only commas - Chinese",
			input:    "，，",
			expected: []string{},
		},
		{
			name:     "Mixed commas with empty parts",
			input:    "123,,456，，789",
			expected: []string{"123", "456", "789"},
		},
		{
			name:     "Complex mixed values",
			input:    "user1@example.com，user2@test.com, admin@domain.org",
			expected: []string{"user1@example.com", "user2@test.com", "admin@domain.org"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f FlexibleStringSlice
			err := f.UnmarshalText([]byte(tt.input))
			if err != nil {
				t.Fatalf("UnmarshalText(%q) error = %v", tt.input, err)
			}

			if tt.expected == nil {
				if f != nil {
					t.Errorf("UnmarshalText(%q) = %v, want nil", tt.input, f)
				}
				return
			}

			if len(f) != len(tt.expected) {
				t.Errorf("UnmarshalText(%q) length = %d, want %d", tt.input, len(f), len(tt.expected))
				return
			}

			for i, v := range tt.expected {
				if f[i] != v {
					t.Errorf("UnmarshalText(%q)[%d] = %q, want %q", tt.input, i, f[i], v)
				}
			}
		})
	}
}

// TestFlexibleStringSlice_UnmarshalText_EmptySliceConsistency tests nil vs empty slice behavior
func TestFlexibleStringSlice_UnmarshalText_EmptySliceConsistency(t *testing.T) {
	t.Run("Empty string returns nil", func(t *testing.T) {
		var f FlexibleStringSlice
		err := f.UnmarshalText([]byte(""))
		if err != nil {
			t.Fatalf("UnmarshalText error = %v", err)
		}
		if f != nil {
			t.Errorf("Empty string should return nil, got %v", f)
		}
	})

	t.Run("Commas only returns empty slice", func(t *testing.T) {
		var f FlexibleStringSlice
		err := f.UnmarshalText([]byte(",,,"))
		if err != nil {
			t.Fatalf("UnmarshalText error = %v", err)
		}
		if f == nil {
			t.Error("Commas only should return empty slice, not nil")
		}
		if len(f) != 0 {
			t.Errorf("Expected empty slice, got %v", f)
		}
	})
}

func TestMCPHost_DefaultAutoEnable(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.MCPHost.AutoEnable {
		t.Error("expected MCPHost.AutoEnable to default to true")
	}
	if cfg.MCPHost.Enabled {
		t.Error("expected MCPHost.Enabled to default to false")
	}
}

func TestValidateMountName(t *testing.T) {
	ok := []string{"notes", "notes-eric", "a1", "X-9"}
	bad := []string{"notes eric", "notes/sub", "notes.md", "no_tes", "", "files", "skills", "tasks", "common", "Files"}
	for _, n := range ok {
		if err := ValidateMountName(n); err != nil {
			t.Errorf("ValidateMountName(%q) unexpected error: %v", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidateMountName(n); err == nil {
			t.Errorf("ValidateMountName(%q) should have failed", n)
		}
	}
}

func TestMCPClientEffectivelyEnabled(t *testing.T) {
	srv := func(enabled bool) map[string]MCPServerConfig {
		return map[string]MCPServerConfig{"s": {Enabled: enabled, Type: "http", URL: "http://x"}}
	}
	cases := []struct {
		name       string
		enabled    bool
		autoEnable bool
		servers    map[string]MCPServerConfig
		want       bool
	}{
		{"master on wins", true, false, nil, true},
		{"auto + enabled server", false, true, srv(true), true},
		{"auto + no enabled server", false, true, srv(false), false},
		{"auto off, server enabled", false, false, srv(true), false},
		{"all off", false, true, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tc := ToolsConfig{MCP: MCPConfig{
				ToolConfig: ToolConfig{Enabled: c.enabled},
				AutoEnable: c.autoEnable,
				Servers:    c.servers,
			}}
			if got := tc.MCPClientEffectivelyEnabled(); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestMCPHostEffectivelyEnabled(t *testing.T) {
	cliProviders := []Provider{
		{Name: "claude-cli", Protocol: "claude-cli"},
		{Name: "codex-cli", Protocol: "codex-cli"},
		{Name: "gemini-cli", Protocol: "gemini-cli"},
		{Name: "openai", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1"},
		{Name: "anthropic", Protocol: "anthropic", BaseURL: "https://api.anthropic.com/v1"},
	}
	tests := []struct {
		name       string
		enabled    bool
		autoEnable bool
		providers  []Provider
		models     []ModelConfig
		want       bool
	}{
		{
			name:    "explicit enabled always starts",
			enabled: true,
			want:    true,
		},
		{
			name:       "auto with claude-cli model starts",
			autoEnable: true,
			providers:  cliProviders,
			models: []ModelConfig{
				{ModelName: "sonnet", Model: "claude-sonnet-4.6", Provider: "claude-cli", Enabled: true},
			},
			want: true,
		},
		{
			name:       "auto with codex-cli model starts",
			autoEnable: true,
			providers:  cliProviders,
			models: []ModelConfig{
				{ModelName: "codex", Model: "gpt-5-codex", Provider: "codex-cli", Enabled: true},
			},
			want: true,
		},
		{
			name:       "auto with gemini-cli model starts",
			autoEnable: true,
			providers:  cliProviders,
			models: []ModelConfig{
				{ModelName: "gemini", Model: "gemini-2.5", Provider: "gemini-cli", Enabled: true},
			},
			want: true,
		},
		{
			name:       "auto with only HTTP models does not start",
			autoEnable: true,
			providers:  cliProviders,
			models: []ModelConfig{
				{ModelName: "gpt", Model: "gpt-4o", Provider: "openai", Enabled: true},
				{ModelName: "claude", Model: "claude-sonnet-4.6", Provider: "anthropic", Enabled: true},
			},
			want: false,
		},
		{
			name:       "auto disabled with cli model does not start",
			autoEnable: false,
			providers:  cliProviders,
			models: []ModelConfig{
				{ModelName: "sonnet", Model: "claude-sonnet-4.6", Provider: "claude-cli", Enabled: true},
			},
			want: false,
		},
		{
			name:       "disabled cli model does not trigger auto-start",
			autoEnable: true,
			providers:  cliProviders,
			models: []ModelConfig{
				{ModelName: "sonnet", Model: "claude-sonnet-4.6", Provider: "claude-cli", Enabled: false},
			},
			want: false,
		},
		{
			name: "all defaults off with no models",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				MCPHost: MCPHostConfig{
					Enabled:    tc.enabled,
					AutoEnable: tc.autoEnable,
				},
				Providers: tc.providers,
				Models:    tc.models,
			}
			if got := cfg.MCPHostEffectivelyEnabled(); got != tc.want {
				t.Errorf("MCPHostEffectivelyEnabled()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfigReloadInterval(t *testing.T) {
	defaultSecs := global.DefaultConfigReloadIntervalSeconds
	minSecs := global.MinConfigReloadIntervalSeconds

	tests := []struct {
		name    string
		seconds int
		want    time.Duration
	}{
		{"unset falls back to default", 0, time.Duration(defaultSecs) * time.Second},
		{"negative falls back to default", -3, time.Duration(defaultSecs) * time.Second},
		{"honours explicit value at floor", minSecs, time.Duration(minSecs) * time.Second},
		{"honours explicit value", 12, 12 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{ConfigReloadIntervalSeconds: tc.seconds}
			if got := cfg.ConfigReloadInterval(); got != tc.want {
				t.Errorf("ConfigReloadInterval()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultConfig_ConfigReloadInterval(t *testing.T) {
	cfg := DefaultConfig()
	want := time.Duration(global.DefaultConfigReloadIntervalSeconds) * time.Second
	if got := cfg.ConfigReloadInterval(); got != want {
		t.Errorf("default ConfigReloadInterval()=%v, want %v", got, want)
	}
}

func TestTurnToolProgressDefaults(t *testing.T) {
	var d AgentDefaults // all zero
	if got := d.GetTurnTimeout(); got != DefaultTurnTimeout {
		t.Errorf("GetTurnTimeout()=%v, want default %v", got, DefaultTurnTimeout)
	}
	if got := d.GetToolTimeout(); got != DefaultToolTimeout {
		t.Errorf("GetToolTimeout()=%v, want default %v", got, DefaultToolTimeout)
	}
	if got := d.GetProgressInterval(); got != DefaultProgressInterval {
		t.Errorf("GetProgressInterval()=%v, want default %v", got, DefaultProgressInterval)
	}

	// Explicit values (seconds) override the defaults.
	d = AgentDefaults{TurnTimeout: 600, ToolTimeout: 90, ProgressInterval: 10}
	if got := d.GetTurnTimeout(); got != 600*time.Second {
		t.Errorf("GetTurnTimeout()=%v, want 10m", got)
	}
	if got := d.GetToolTimeout(); got != 90*time.Second {
		t.Errorf("GetToolTimeout()=%v, want 90s", got)
	}
	if got := d.GetProgressInterval(); got != 10*time.Second {
		t.Errorf("GetProgressInterval()=%v, want 10s", got)
	}

	// A negative progress interval disables progress updates.
	d = AgentDefaults{ProgressInterval: -1}
	if got := d.GetProgressInterval(); got != 0 {
		t.Errorf("GetProgressInterval()=%v, want 0 (disabled)", got)
	}
}

func TestCooldownConfigDurations(t *testing.T) {
	// All zero → built-in defaults (minutes).
	var c CooldownConfig
	if c.BillingAuth() != 60*time.Minute {
		t.Errorf("BillingAuth default = %v, want 60m", c.BillingAuth())
	}
	if c.RateLimit() != 10*time.Minute || c.ClientError() != 10*time.Minute || c.ServerError() != 10*time.Minute {
		t.Errorf("rate/client/server defaults wrong: %v %v %v", c.RateLimit(), c.ClientError(), c.ServerError())
	}
	if c.BadRequest() != 1*time.Minute {
		t.Errorf("BadRequest default = %v, want 1m", c.BadRequest())
	}

	// Explicit value overrides; negative disables.
	c = CooldownConfig{BillingAuthMinutes: 1440, RateLimitMinutes: -1}
	if c.BillingAuth() != 1440*time.Minute {
		t.Errorf("BillingAuth = %v, want 24h", c.BillingAuth())
	}
	if c.RateLimit() != 0 {
		t.Errorf("negative RateLimit should disable (0), got %v", c.RateLimit())
	}
}
