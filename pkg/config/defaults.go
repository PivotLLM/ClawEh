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
		// Marks this as an auto-seeded config the user hasn't saved yet; the first
		// SaveConfig clears it. SeedDefaultConfig preserves it when written to disk.
		DefaultConfig: true,
		Agents: AgentsConfig{
			BaseDir: agentsBaseDir,
			Defaults: AgentDefaults{
				RestrictToWorkspace:  true,
				WorkspaceWriteSubdir: "files",
				WorkspaceReadSubdirs: []string{"files", "skills"},
				Memory: MemoryConfig{
					Prompt: MemoryPromptConfig{
						TopKDomains: 3, MaxChars: 4000, MinConfidence: 0.65,
						IncludeDebugTrace: false, PendingSurface: "ask", PendingMax: 8,
					},
					Consolidation: MemoryConsolidationConfig{
						EveryNMessages: 50, IdleMinutes: 60, Nightly: true, NightlyAt: "03:20",
						ProposeDomains: true, AutoPromote: false, DebugDump: false,
						MaxBatchMessages: 200, MaxInputTokens: 96000, PerMessageChars: 12000,
						MaxOutputTokens: 8000, MaxRuntimeSecs: 120,
					},
					Retention: MemoryRetentionConfig{ProtectUnconsolidated: true},
					Export:    MemoryExportConfig{Enabled: true},
				},
				Models:               []string{"Claude CLI", "Codex CLI"},
				MaxTokens:            32768,
				Temperature:          nil, // nil means use provider default
				MaxToolIterations:    50,
				RequestTimeout:       300, // global default (s); per-model overrides (CLIs set longer)
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
			// CLI providers run a local binary found on PATH; set `command` to
			// override the path. base_url is unused for CLI protocols.
			{Name: "Claude CLI", Protocol: "claude-cli"},
			{Name: "Codex CLI", Protocol: "codex-cli"},
			{Name: "Gemini CLI", Protocol: "gemini-cli"},

			{Name: "OpenAI", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1"},
			{Name: "Anthropic", Protocol: "anthropic", BaseURL: "https://api.anthropic.com/v1"},
			{Name: "DeepSeek", Protocol: "openai-chat", BaseURL: "https://api.deepseek.com/v1"},
			{Name: "Google API", Protocol: "openai-chat", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"},
			{Name: "Qwen", Protocol: "openai-chat", BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"},
			{Name: "Moonshot", Protocol: "openai-chat", BaseURL: "https://api.moonshot.ai/v1"},
			{Name: "Groq", Protocol: "openai-chat", BaseURL: "https://api.groq.com/openai/v1", NoParallelToolCalls: true},
			{Name: "OpenRouter Chat", Protocol: "openai-chat", BaseURL: "https://openrouter.ai/api/v1"},
			{Name: "OpenRouter Responses", Protocol: "openai-responses", BaseURL: "https://openrouter.ai/api/v1"},
			{Name: "OpenRouter Strict", Protocol: "openai-chat", BaseURL: "https://openrouter.ai/api/v1", StrictCompat: true},
			{Name: "NVIDIA", Protocol: "openai-chat", BaseURL: "https://integrate.api.nvidia.com/v1"},
			{Name: "Cerebras", Protocol: "openai-chat", BaseURL: "https://api.cerebras.ai/v1"},
			{Name: "Ollama", Protocol: "openai-chat", BaseURL: "http://localhost:11434/v1"},
			{Name: "Mistral", Protocol: "openai-chat", BaseURL: "https://api.mistral.ai/v1"},
			{Name: "Avian", Protocol: "openai-chat", BaseURL: "https://api.avian.io/v1"},
			{Name: "vLLM Local", Protocol: "openai-chat", BaseURL: "http://localhost:8000/v1"},
			{Name: "Bedrock Chat", Protocol: "openai-chat", BaseURL: "https://bedrock-mantle.us-east-1.api.aws/v1"},
			{Name: "Bedrock Responses", Protocol: "openai-responses", BaseURL: "https://bedrock-mantle.us-east-1.api.aws/openai/v1"},
			{Name: "Abliteration", Protocol: "openai-chat", BaseURL: "https://api.abliteration.ai/v1"},
			{Name: "xAI", Protocol: "openai-chat", BaseURL: "https://api.x.ai/v1"},
		},
		Models: []ModelConfig{
			// ============================================
			// A starter catalogue of tested providers/models, ALL DISABLED.
			// Enable a model and add its provider's api_key to use it. CLI models
			// need the matching binary on PATH. `model` is the raw id the endpoint
			// expects; `provider` names the endpoint above.
			// ============================================

			// CLI providers (local binaries).
			{ModelName: "Claude CLI", Model: "claude-cli", Provider: "Claude CLI", RequestTimeout: 3600, ExtraArgs: []string{"--dangerously-skip-permissions", "--no-chrome"}, Env: map[string]string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1"}, Enabled: false},
			{ModelName: "Claude CLI Opus", Model: "claude-opus-4-7", Provider: "Claude CLI", RequestTimeout: 3600, ExtraArgs: []string{"--dangerously-skip-permissions", "--no-chrome"}, Env: map[string]string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1"}, Enabled: false},
			{ModelName: "Codex CLI", Model: "codex-cli", Provider: "Codex CLI", RequestTimeout: 3600, ExtraArgs: []string{"--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check"}, Enabled: false},
			{ModelName: "Gemini CLI", Model: "gemini-2.5-pro", Provider: "Gemini CLI", RequestTimeout: 3600, ExtraArgs: []string{"--yolo"}, Enabled: false},

			// HTTP providers.
			{ModelName: "OpenAI GPT 5.5", Model: "gpt-5.5", Provider: "OpenAI", DropParams: []string{"temperature"}, Enabled: false},
			{ModelName: "Claude Sonnet 4.6", Model: "claude-sonnet-4.6", Provider: "Anthropic", Enabled: false},
			{ModelName: "DeepSeek Chat", Model: "deepseek-chat", Provider: "DeepSeek", Enabled: false},
			{ModelName: "Gemini 3.5 Flash", Model: "gemini-3.5-flash", Provider: "Google API", Enabled: false},
			{ModelName: "Qwen Plus", Model: "qwen-plus", Provider: "Qwen", Enabled: false},
			{ModelName: "Moonshot v1 8k", Model: "moonshot-v1-8k", Provider: "Moonshot", Enabled: false},
			{ModelName: "Groq Llama 3.3 70b", Model: "llama-3.3-70b-versatile", Provider: "Groq", NoTools: true, Enabled: false},
			{ModelName: "Groq GPT OSS 120b", Model: "openai/gpt-oss-120b", Provider: "Groq", Enabled: false},
			{ModelName: "OpenRouter Auto", Model: "openrouter/auto", Provider: "OpenRouter Chat", Enabled: false},
			{ModelName: "OpenRouter GPT 5.4", Model: "openai/gpt-5.4", Provider: "OpenRouter Strict", DropParams: []string{"temperature"}, Enabled: false},
			{ModelName: "NVIDIA Nemotron 4 340b", Model: "nemotron-4-340b-instruct", Provider: "NVIDIA", Enabled: false},
			{ModelName: "Cerebras Llama 3.3 70b", Model: "llama-3.3-70b", Provider: "Cerebras", Enabled: false},
			{ModelName: "Llama 3", Model: "llama3", Provider: "Ollama", Enabled: false},
			{ModelName: "Mistral Small", Model: "mistral-small-latest", Provider: "Mistral", Enabled: false},
			{ModelName: "DeepSeek v3.2", Model: "deepseek/deepseek-v3.2", Provider: "Avian", Enabled: false},
			{ModelName: "Kimi k2.5", Model: "moonshotai/kimi-k2.5", Provider: "Avian", Enabled: false},
			{ModelName: "Local Model", Model: "custom-model", Provider: "vLLM Local", Enabled: false},
			{ModelName: "Bedrock Opus 4.8", Model: "anthropic.claude-opus-4-8", Provider: "Bedrock Chat", Enabled: false},
			{ModelName: "Bedrock Sonnet 4.6", Model: "anthropic.claude-sonnet-4-6", Provider: "Bedrock Chat", Enabled: false},
			{ModelName: "Bedrock GPT 5.5", Model: "openai.gpt-5.5", Provider: "Bedrock Responses", DropParams: []string{"temperature"}, Enabled: false},
			{ModelName: "Bedrock DeepSeek 3", Model: "deepseek.v3.2", Provider: "Bedrock Chat", Enabled: false},
			{ModelName: "Bedrock Nova 2 Lite", Model: "amazon.nova-2-lite-v1:0", Provider: "Bedrock Chat", Enabled: false},
			{ModelName: "Bedrock Gemma 3", Model: "google.gemma-3-27b-it", Provider: "Bedrock Chat", MaxTokens: 5000, StrictAlternation: true, Enabled: false},
			{ModelName: "Abliteration", Model: "abliterated-model", Provider: "Abliteration", Enabled: false},
			{ModelName: "OpenRouter Elephant", Model: "elephant-alpha", Provider: "OpenRouter Chat", Enabled: false},
			{ModelName: "Gemini 3.1 Flash Lite Preview", Model: "google/gemini-3.1-flash-lite-preview", Provider: "OpenRouter Chat", Enabled: false},
			{ModelName: "OpenRouter Llama 4 Maverick", Model: "meta-llama/llama-4-maverick", Provider: "OpenRouter Chat", Enabled: false},
			{ModelName: "OpenRouter Llama 4 Scout", Model: "meta-llama/llama-4-scout", Provider: "OpenRouter Chat", Enabled: false},
			{ModelName: "OpenRouter DeepSeek V4 Flash", Model: "deepseek/deepseek-v4-flash", Provider: "OpenRouter Chat", Enabled: false},
			{ModelName: "OpenRouter DeepSeek V4 Pro", Model: "deepseek/deepseek-v4-pro", Provider: "OpenRouter Chat", Enabled: false},
			{ModelName: "Grok 4.3", Model: "grok-4.3", Provider: "xAI", Enabled: false},
			{ModelName: "Grok 4.3 High", Model: "grok-4.3", Provider: "xAI", ReasoningEffort: "high", Enabled: false},
			{ModelName: "Grok 4.3 Medium", Model: "grok-4.3", Provider: "xAI", ReasoningEffort: "medium", Enabled: false},
			{ModelName: "OpenRouter GPT 5.5", Model: "openai/gpt-5.5", Provider: "OpenRouter Strict", DropParams: []string{"temperature"}, Enabled: false},
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
					Enabled: true,
				},
				AutoEnable: true,
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
				MaxReadFileSize: 32 * 1024, // 32KB (~8K tokens) per read
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
			File:          global.DefaultLogFile,
			Console:       global.DefaultLogConsole,
			Level:         global.DefaultLogLevel,
			JSON:          global.DefaultLogJSON,
			RetentionDays: global.DefaultLogRetentionDays,
			DumpRefusals:  true,
			DumpAll:       false,
		},
		MCPHost: MCPHostConfig{
			Enabled:      false,
			AutoEnable:   true,
			Listen:       "127.0.0.1:5911",
			EndpointPath: "/mcp",
			// InternalTools/ExternalTools are intentionally left unset: when empty,
			// each endpoint's catalogue mirrors the internal API path — it advertises
			// every tool any agent is allowed, gated PER AGENT at execution time via
			// the session_token. Set an explicit per-endpoint list only to narrow
			// (coarsely) what /internal or /mcp advertises.
			InternalTools: nil,
			ExternalTools: nil,
		},
		ConfigReloadIntervalSeconds: global.DefaultConfigReloadIntervalSeconds,
	}
	cfg.dataDir = homePath
	// Ensure agents/default directory exists on startup
	os.MkdirAll(filepath.Join(homePath, "agents", "default"), 0o755)
	return cfg
}
