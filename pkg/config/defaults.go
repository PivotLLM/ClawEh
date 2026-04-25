// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"os"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

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
	workspacePath := filepath.Join(homePath, "agents", "default")

	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Workspace:           workspacePath,
				RestrictToWorkspace: true,
				Model: &AgentModelConfig{
					Primary:   "openrouter-gpt-5.4",
					Fallbacks: []string{"claude-cli"},
				},
				MaxTokens:                 32768,
				Temperature:               nil, // nil means use provider default
				MaxToolIterations:         50,
				SummarizeMessageThreshold: 20,
				SummarizeTokenPercent:     75,
				ContextWindow:             128000,
			},
			List: []AgentConfig{
				{
					ID:      "main",
					Name:    "main",
					Default: true,
					Tools:   []string{"*"},
				},
			},
		},
		Bindings: []AgentBinding{},
		Session: SessionConfig{
			Mode: "unified",
		},
		Channels: ChannelsConfig{
			WhatsApp: WhatsAppConfig{
				Enabled:          false,
				BridgeURL:        "ws://localhost:3001",
				UseNative:        false,
				SessionStorePath: "",
				AllowFrom:        FlexibleStringSlice{},
			},
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
		Providers: ProvidersConfig{
			OpenAI: OpenAIProviderConfig{WebSearch: true},
		},
		ModelList: []ModelConfig{
			// ============================================
			// Add your API key to the model you want to use
			// ============================================

			// OpenAI - https://platform.openai.com/api-keys
			{
				ModelName: "gpt-5.4",
				Model:     "openai/gpt-5.4",
				APIBase:   "https://api.openai.com/v1",
				APIKey:    "",
				Enabled:   true,
			},

			// Anthropic Claude - https://console.anthropic.com/settings/keys
			{
				ModelName: "claude-sonnet-4.6",
				Model:     "anthropic/claude-sonnet-4.6",
				APIBase:   "https://api.anthropic.com/v1",
				APIKey:    "",
				Enabled:   true,
			},

			// DeepSeek - https://platform.deepseek.com/
			{
				ModelName: "deepseek-chat",
				Model:     "deepseek/deepseek-chat",
				APIBase:   "https://api.deepseek.com/v1",
				APIKey:    "",
				Enabled:   true,
			},

			// Google Gemini - https://ai.google.dev/
			{
				ModelName: "gemini-2.0-flash",
				Model:     "gemini/gemini-2.0-flash-exp",
				APIBase:   "https://generativelanguage.googleapis.com/v1beta",
				APIKey:    "",
				Enabled:   true,
			},

			// Qwen (通义千问) - https://dashscope.console.aliyun.com/apiKey
			{
				ModelName: "qwen-plus",
				Model:     "qwen/qwen-plus",
				APIBase:   "https://dashscope.aliyuncs.com/compatible-mode/v1",
				APIKey:    "",
				Enabled:   true,
			},

			// Moonshot (月之暗面) - https://platform.moonshot.cn/console/api-keys
			{
				ModelName: "moonshot-v1-8k",
				Model:     "moonshot/moonshot-v1-8k",
				APIBase:   "https://api.moonshot.cn/v1",
				APIKey:    "",
				Enabled:   true,
			},

			// Groq - https://console.groq.com/keys
			{
				ModelName: "llama-3.3-70b",
				Model:     "groq/llama-3.3-70b-versatile",
				APIBase:   "https://api.groq.com/openai/v1",
				APIKey:    "",
				Enabled:   true,
			},

			// OpenRouter (100+ models) - https://openrouter.ai/keys
			{
				ModelName: "openrouter-auto",
				Model:     "openrouter/auto",
				APIBase:   "https://openrouter.ai/api/v1",
				APIKey:    "",
				Enabled:   true,
			},
			{
				ModelName: "openrouter-gpt-5.4",
				Model:     "openrouter/openai/gpt-5.4",
				APIBase:   "https://openrouter.ai/api/v1",
				APIKey:    "",
				Enabled:   true,
			},

			// NVIDIA - https://build.nvidia.com/
			{
				ModelName: "nemotron-4-340b",
				Model:     "nvidia/nemotron-4-340b-instruct",
				APIBase:   "https://integrate.api.nvidia.com/v1",
				APIKey:    "",
				Enabled:   true,
			},

			// Cerebras - https://inference.cerebras.ai/
			{
				ModelName: "cerebras-llama-3.3-70b",
				Model:     "cerebras/llama-3.3-70b",
				APIBase:   "https://api.cerebras.ai/v1",
				APIKey:    "",
				Enabled:   true,
			},

			// Ollama (local) - https://ollama.com
			{
				ModelName: "llama3",
				Model:     "ollama/llama3",
				APIBase:   "http://localhost:11434/v1",
				APIKey:    "ollama",
				Enabled:   true,
			},

			// Mistral AI - https://console.mistral.ai/api-keys
			{
				ModelName: "mistral-small",
				Model:     "mistral/mistral-small-latest",
				APIBase:   "https://api.mistral.ai/v1",
				APIKey:    "",
				Enabled:   true,
			},

			// Avian - https://avian.io
			{
				ModelName: "deepseek-v3.2",
				Model:     "avian/deepseek/deepseek-v3.2",
				APIBase:   "https://api.avian.io/v1",
				APIKey:    "",
				Enabled:   true,
			},
			{
				ModelName: "kimi-k2.5",
				Model:     "avian/moonshotai/kimi-k2.5",
				APIBase:   "https://api.avian.io/v1",
				APIKey:    "",
				Enabled:   true,
			},

			// VLLM (local) - http://localhost:8000
			{
				ModelName: "local-model",
				Model:     "vllm/custom-model",
				APIBase:   "http://localhost:8000/v1",
				APIKey:    "",
				Enabled:   true,
			},

			// Azure OpenAI - https://portal.azure.com
			// model_name is a user-friendly alias; the model field's path after "azure/" is your deployment name
			{
				ModelName: "azure-gpt5",
				Model:     "azure/my-gpt5-deployment",
				APIBase:   "https://your-resource.openai.azure.com",
				APIKey:    "",
				Enabled:   true,
			},

			// AWS Bedrock - https://aws.amazon.com/bedrock/
			// api_base: region name (e.g. us-east-1) or full endpoint URL
			// api_key: leave empty to use the AWS default credential chain (env vars, ~/.aws, IAM roles)
			//          or set to "ACCESS_KEY_ID:SECRET_ACCESS_KEY" or a Bedrock API key (bearer token)
			// model: use the exact Bedrock model ID from your AWS console, e.g.:
			//   anthropic.claude-3-5-sonnet-20241022-v2:0  (direct access)
			//   us.anthropic.claude-3-5-sonnet-20241022-v2:0  (cross-region inference profile)
			{
				ModelName: "bedrock-claude-sonnet",
				Model:     "bedrock/us.anthropic.claude-sonnet-4-20250514-v1:0",
				APIBase:   "us-east-1",
				APIKey:    "",
				Enabled:   true,
			},

			// Claude CLI (local) - https://claude.ai/download
			{
				ModelName:      "claude-cli",
				Model:          "claude-cli/claude-cli",
				Workspace:      ".",
				RequestTimeout: 3600,
				Enabled:        false,
			},

			// Codex CLI (local) - https://github.com/openai/codex
			{
				ModelName:      "codex-cli",
				Model:          "codex-cli/codex-cli",
				Workspace:      ".",
				RequestTimeout: 3600,
				Enabled:        false,
			},

			// Gemini CLI (local) - https://github.com/google-gemini/gemini-cli
			{
				ModelName:      "gemini-cli",
				Model:          "gemini-cli/gemini-cli",
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
				AllowRemote:        true,
				TimeoutSeconds:     60,
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
			SendFile: ToolConfig{
				Enabled: true,
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
			AppendFile: ToolConfig{
				Enabled: true,
			},
			EditFile: ToolConfig{
				Enabled: true,
			},
			FindSkills: ToolConfig{
				Enabled: true,
			},
			I2C: ToolConfig{
				Enabled: false, // Hardware tool - Linux only
			},
			InstallSkill: ToolConfig{
				Enabled: true,
			},
			ListDir: ToolConfig{
				Enabled: true,
			},
			Message: ToolConfig{
				Enabled: true,
			},
			ReadFile: ReadFileToolConfig{
				Enabled:         true,
				MaxReadFileSize: 64 * 1024, // 64KB
			},
			Spawn: ToolConfig{
				Enabled: true,
			},
			SPI: ToolConfig{
				Enabled: false, // Hardware tool - Linux only
			},
			Subagent: ToolConfig{
				Enabled: true,
			},
			WebFetch: ToolConfig{
				Enabled: true,
			},
			WriteFile: ToolConfig{
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
			File:    global.DefaultLogFile,
			Console: global.DefaultLogConsole,
			Level:   global.DefaultLogLevel,
			JSON:    global.DefaultLogJSON,
		},
		MCPHost: MCPHostConfig{
			Enabled:      false,
			AutoEnable:   true,
			Listen:       "127.0.0.1:5911",
			EndpointPath: "/mcp",
			Tools: []string{
				"read_file",
				"write_file",
				"edit_file",
				"append_file",
				"list_dir",
				"web_fetch",
				"web_search",
				"send_file",
			},
		},
		ConfigReloadIntervalSeconds: global.DefaultConfigReloadIntervalSeconds,
	}
	cfg.dataDir = homePath
	// Ensure agents/default directory exists on startup
	os.MkdirAll(filepath.Join(homePath, "agents", "default"), 0o755)
	return cfg
}
