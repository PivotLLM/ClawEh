// ClawEh - Personal AI Assistant
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"os"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

// DefaultAgentTools is the baseline tool allowlist used when an agent has no
// explicit tools list. Populated at startup from provider descriptors via
// SetDefaultAgentTools; the value here is a safe fallback only.
var DefaultAgentTools = []string{"*"}

// SetDefaultAgentTools replaces the default tool allowlist. Called from the
// gateway after all tool providers are registered, so each provider's
// DefaultEnabled flag drives which tools are in every agent's baseline.
func SetDefaultAgentTools(names []string) {
	if len(names) > 0 {
		DefaultAgentTools = names
	}
}

// DefaultConfig returns the default configuration for ClawEh.
func DefaultConfig() *Config {
	// Determine the base path for the workspace.
	// Priority: $CLAW_HOME > ~/.claw
	var homePath string
	if clawHome := os.Getenv(global.EnvVarHome); clawHome != "" {
		homePath = clawHome
	} else {
		userHome, _ := os.UserHomeDir()
		homePath = filepath.Join(userHome, global.DefaultDataDir)
	}
	agentsBaseDir := filepath.Join(homePath, "agents")
	workspacePath := filepath.Join(agentsBaseDir, "default")

	cfg := &Config{
		Agents: AgentsConfig{
			BaseDir: agentsBaseDir,
			Defaults: AgentDefaults{
				RestrictToWorkspace:  true,
				Model:                &AgentModelConfig{Primary: "claude-cli", Fallbacks: []string{"codex-cli"}},
				MaxTokens:            32768,
				Temperature:          nil, // nil means use provider default
				MaxToolIterations:    50,
				ContextWindow:        128000,
				ArchiveDays:          365,
				SummaryRetentionDays: 3650,
			},
			List: []AgentConfig{
				{
					ID:        "claw",
					Name:      "Claw",
					Default:   true,
					Workspace: workspacePath,
					Tools:     []string{"*"},
				},
			},
		},
		Bindings: []AgentBinding{},
		Session: SessionConfig{
			Mode: "unified",
		},
		Channels: ChannelsConfig{
			Telegram: []TelegramBotConfig{
				{
					ID:        "default",
					Enabled:   false,
					Token:     "",
					AllowFrom: FlexibleStringSlice{},
					Typing:    TypingConfig{Enabled: true},
					Placeholder: PlaceholderConfig{
						Enabled: true,
						Text:    "Thinking... 💭",
					},
					// Enabled left nil → on by default (see CoalesceConfig.IsEnabled).
					Coalesce: CoalesceConfig{
						WindowMS: DefaultCoalesceWindowMS,
					},
				},
			},
			Discord: DiscordConfig{
				Enabled:     false,
				Token:       "",
				AllowFrom:   FlexibleStringSlice{},
				MentionOnly: false,
			},
			Slack: SlackConfig{
				Enabled:   false,
				BotToken:  "",
				AppToken:  "",
				AllowFrom: FlexibleStringSlice{},
			},
			Matrix: MatrixConfig{
				Enabled:      false,
				Homeserver:   "https://matrix.org",
				UserID:       "",
				AccessToken:  "",
				DeviceID:     "",
				JoinOnInvite: true,
				AllowFrom:    FlexibleStringSlice{},
				GroupTrigger: GroupTriggerConfig{
					MentionOnly: true,
				},
				Placeholder: PlaceholderConfig{
					Enabled: true,
					Text:    "Thinking... 💭",
				},
			},
			LINE: LINEConfig{
				Enabled:            false,
				ChannelSecret:      "",
				ChannelAccessToken: "",
				WebhookHost:        "0.0.0.0",
				WebhookPort:        18791,
				WebhookPath:        "/webhook/line",
				AllowFrom:          FlexibleStringSlice{},
				GroupTrigger:       GroupTriggerConfig{MentionOnly: true},
			},
			WebUI: WebUIConfig{
				Enabled:        false,
				Token:          "",
				PingInterval:   30,
				ReadTimeout:    60,
				WriteTimeout:   10,
				MaxConnections: 100,
				AllowFrom:      FlexibleStringSlice{},
			},
		},
		// Named endpoints. A model references one by name; the WebUI groups
		// models by provider. Add your API key to the provider you want to use.
		Providers: []Provider{
			{Name: "openai", Protocol: "openai", BaseURL: "https://api.openai.com/v1"},
			{Name: "anthropic", Protocol: "anthropic", BaseURL: "https://api.anthropic.com/v1"},
			{Name: "openrouter", Protocol: "openai", BaseURL: "https://openrouter.ai/api/v1"},
			{Name: "groq", Protocol: "openai", BaseURL: "https://api.groq.com/openai/v1"},
			{Name: "deepseek", Protocol: "openai", BaseURL: "https://api.deepseek.com/v1"},
			{Name: "gemini", Protocol: "openai", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"},
			{Name: "mistral", Protocol: "openai", BaseURL: "https://api.mistral.ai/v1"},
			{Name: "ollama", Protocol: "openai", BaseURL: "http://localhost:11434/v1", APIKey: "ollama"},
			{Name: "claude-cli", Protocol: "claude-cli"},
			{Name: "codex-cli", Protocol: "codex-cli"},
			{Name: "gemini-cli", Protocol: "gemini-cli"},
		},
		Models: []ModelConfig{
			// ============================================
			// Enable a model and ensure its provider has an API key.
			// model is the raw id the endpoint expects; provider names the endpoint.
			// ============================================

			{ModelName: "gpt-5.4", Model: "gpt-5.4", Provider: "openai", Enabled: false},
			{ModelName: "claude-sonnet-4.6", Model: "claude-sonnet-4.6", Provider: "anthropic", Enabled: false},
			{ModelName: "openrouter-auto", Model: "openrouter/auto", Provider: "openrouter", Enabled: false},
			{ModelName: "deepseek-chat", Model: "deepseek-chat", Provider: "deepseek", Enabled: false},
			{ModelName: "llama-3.3-70b", Model: "llama-3.3-70b-versatile", Provider: "groq", Enabled: false},
			{ModelName: "mistral-small", Model: "mistral-small-latest", Provider: "mistral", Enabled: false},
			{ModelName: "llama3", Model: "llama3", Provider: "ollama", Enabled: false},

			// Claude CLI (local) - https://claude.ai/download
			{
				ModelName:      "claude-cli",
				Model:          "claude-cli",
				Provider:       "claude-cli",
				Workspace:      ".",
				RequestTimeout: 3600,
				ExtraArgs:      []string{"--dangerously-skip-permissions", "--no-chrome"},
				Env:            map[string]string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1"},
				Enabled:        false,
			},

			// Codex CLI (local) - https://github.com/openai/codex
			{
				ModelName:      "codex-cli",
				Model:          "codex-cli",
				Provider:       "codex-cli",
				Workspace:      ".",
				RequestTimeout: 3600,
				Enabled:        false,
			},

			// Gemini CLI (local) - https://github.com/google-gemini/gemini-cli
			{
				ModelName:      "gemini-cli",
				Model:          "gemini-cli",
				Provider:       "gemini-cli",
				Workspace:      ".",
				RequestTimeout: 3600,
				Enabled:        false,
			},
		},
		Gateway: GatewayConfig{
			Host: "127.0.0.1",
			Port: 18790,
		},
		Tools: ToolsConfig{
			MediaCleanup: MediaCleanupConfig{
				ToolConfig: ToolConfig{
					Enabled: true,
				},
				MaxAge:   30,
				Interval: 5,
			},
			Web: WebToolsConfig{
				ToolConfig: ToolConfig{
					Enabled: true,
				},
				Proxy:           "",
				FetchLimitBytes: 10 * 1024 * 1024, // 10MB by default
				Brave: BraveConfig{
					Enabled:    false,
					APIKey:     "",
					APIKeys:    nil,
					MaxResults: 5,
				},
				Tavily: TavilyConfig{
					Enabled:    false,
					APIKey:     "",
					APIKeys:    nil,
					MaxResults: 5,
				},
				DuckDuckGo: DuckDuckGoConfig{
					Enabled:    false,
					MaxResults: 5,
				},
				Perplexity: PerplexityConfig{
					Enabled:    false,
					APIKey:     "",
					APIKeys:    nil,
					MaxResults: 5,
				},
				SearXNG: SearXNGConfig{
					Enabled:    false,
					BaseURL:    "",
					MaxResults: 5,
				},
				GLMSearch: GLMSearchConfig{
					Enabled:      false,
					APIKey:       "",
					BaseURL:      "https://open.bigmodel.cn/api/paas/v4/web_search",
					SearchEngine: "search_std",
					MaxResults:   5,
				},
			},
			Cron: CronToolsConfig{
				ToolConfig: ToolConfig{
					Enabled: true,
				},
				ExecTimeoutMinutes: 5,
			},
			Exec: ExecConfig{
				ToolConfig: ToolConfig{
					Enabled: true,
				},
				EnableDenyPatterns: true,
				// Off by default: shell_exec is restricted to internal channels
				// (cli/system/subagent/recovery). Opt in to allow exec from remote
				// channels such as Telegram or the WebUI chat. See GHSA-pv8c-p6jf-3fpp.
				AllowRemote:    false,
				TimeoutSeconds: 60,
			},
			Skills: SkillsToolsConfig{
				Local: ToolConfig{
					Enabled: true,
				},
				Registry: ToolConfig{
					Enabled: false,
				},
				Registries: SkillsRegistriesConfig{
					ClawHub: ClawHubRegistryConfig{
						Enabled: false,
						BaseURL: "https://clawhub.ai",
					},
				},
				MaxConcurrentSearches: 2,
				SearchCache: SearchCacheConfig{
					MaxSize:    50,
					TTLSeconds: 300,
				},
			},
			MCP: MCPConfig{
				ToolConfig: ToolConfig{
					Enabled: false,
				},
				Discovery: ToolDiscoveryConfig{
					Enabled:          false,
					TTL:              5,
					MaxSearchResults: 5,
					UseBM25:          true,
					UseRegex:         false,
				},
				Servers: map[string]MCPServerConfig{},
			},
			ReadFile: ReadFileToolConfig{
				Enabled:         true,
				MaxReadFileSize: 64 * 1024, // 64KB
			},
			Subagent: ToolConfig{
				Enabled: true,
			},
		},
		Devices: DevicesConfig{
			Enabled:    false,
			MonitorUSB: true,
		},
		Voice: VoiceConfig{
			EchoTranscription: false,
		},
		Logging: LoggingConfig{
			File:         global.DefaultLogFile,
			Console:      global.DefaultLogConsole,
			Level:        global.DefaultLogLevel,
			JSON:         global.DefaultLogJSON,
			DumpRefusals: true,
			DumpAll:      false,
		},
		MCPHost: MCPHostConfig{
			Enabled:      false,
			AutoEnable:   true,
			Listen:       "127.0.0.1:5911",
			EndpointPath: "/mcp",
			// Tools is intentionally left unset: when empty, the MCP host exposes
			// the DefaultEnabled tool set (same source as the per-agent default
			// allowlist), so marking a tool DefaultEnabled exposes it everywhere.
			// Set an explicit list only to expose a narrower subset.
			Tools: nil,
		},
		ConfigReloadIntervalSeconds: global.DefaultConfigReloadIntervalSeconds,
	}
	cfg.dataDir = homePath
	// Ensure agents/default directory exists on startup
	os.MkdirAll(filepath.Join(homePath, "agents", "default"), 0o755)
	return cfg
}
