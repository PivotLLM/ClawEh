package config

import (
	"encoding/json"
)

// DiscoveryTTLMax is the longest a revealed tool stays visible without use, in
// turns (reset on each use); falls back to the default when unset.
func (c *Config) DiscoveryTTLMax() int {
	if c.Tools.Discovery.TTLMax <= 0 {
		return DefaultDiscoveryTTLMax
	}
	return c.Tools.Discovery.TTLMax
}

// DiscoveryVisibleBudget is the max number of revealed tools allowed visible at
// once before lowest-TTL-first pruning kicks in; falls back to the default when
// unset.
func (c *Config) DiscoveryVisibleBudget() int {
	if c.Tools.Discovery.VisibleBudget <= 0 {
		return DefaultDiscoveryVisibleBudget
	}
	return c.Tools.Discovery.VisibleBudget
}

type ToolDiscoveryConfig struct {
	// Enabled is the single global switch for progressive tool discovery, applied
	// uniformly to every agent's IN-LOOP tool list. Default OFF. When on,
	// discovery-eligible tools (the fusion and maestro suites and all upstream MCP
	// tools) are hidden behind the search_tools / get_tool_details meta-tools;
	// native tools and the cogmem suite stay always-on. The MCP host is never
	// subject to discovery — external clients always receive the full tool list.
	Enabled bool `json:"enabled" env:"CLAW_TOOLS_DISCOVERY_ENABLED"`
	// AlwaysShownNamespaces pins discovery-eligible tool namespaces (matched by
	// MatchVisibility: "file", "maestro", "fusion", a "<server>" for an upstream MCP
	// server, or "*") so they stay in the in-loop model's tool list even when
	// discovery is on, instead of being hidden behind search_tools /
	// get_tool_details. Native tools and cogmem are always shown by rule and need
	// not be listed. Empty pins nothing; only consulted when Enabled is true.
	AlwaysShownNamespaces []string `json:"always_shown_namespaces,omitempty"`
	// TTLMax is the longest a revealed tool stays visible without being used, in
	// turns; each use resets it. A tool idle this many turns is hidden again.
	TTLMax int `json:"ttl_max" env:"CLAW_TOOLS_DISCOVERY_TTL_MAX"`
	// VisibleBudget caps how many revealed (non-core) tools may be visible at once.
	// Under the cap every tool lives to TTLMax; when a new reveal pushes the count
	// over it, the tools with the smallest remaining TTL are hidden until back at
	// the cap. Keeps a fanned-out working set bounded without churning small ones.
	VisibleBudget int `json:"visible_budget" env:"CLAW_TOOLS_DISCOVERY_VISIBLE_BUDGET"`
	// MaxSearchResults caps search_tools results.
	MaxSearchResults int `json:"max_search_results" env:"CLAW_MAX_SEARCH_RESULTS"`
}

// UnmarshalJSON normalizes the retired "ttl" key onto TTLMax so a config written
// before the rename keeps working; an explicit "ttl_max" always wins.
func (d *ToolDiscoveryConfig) UnmarshalJSON(data []byte) error {
	type alias ToolDiscoveryConfig
	aux := &struct {
		LegacyTTL *int `json:"ttl"`
		*alias
	}{alias: (*alias)(d)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if d.TTLMax == 0 && aux.LegacyTTL != nil {
		d.TTLMax = *aux.LegacyTTL
	}
	return nil
}

// Discovery defaults, applied when the config value is unset (<= 0).
const (
	DefaultDiscoveryTTLMax        = 50
	DefaultDiscoveryVisibleBudget = 100
	DefaultDiscoveryMaxSearchHits = 10
)

type ToolConfig struct {
	Enabled bool `json:"enabled" env:"ENABLED"`
}

type BraveConfig struct {
	Enabled    bool     `json:"enabled"     env:"CLAW_TOOLS_WEB_BRAVE_ENABLED"`
	APIKey     string   `json:"api_key"     env:"CLAW_TOOLS_WEB_BRAVE_API_KEY"`
	APIKeys    []string `json:"api_keys"    env:"CLAW_TOOLS_WEB_BRAVE_API_KEYS"`
	MaxResults int      `json:"max_results" env:"CLAW_TOOLS_WEB_BRAVE_MAX_RESULTS"`
}

type TavilyConfig struct {
	Enabled    bool     `json:"enabled"     env:"CLAW_TOOLS_WEB_TAVILY_ENABLED"`
	APIKey     string   `json:"api_key"     env:"CLAW_TOOLS_WEB_TAVILY_API_KEY"`
	APIKeys    []string `json:"api_keys"    env:"CLAW_TOOLS_WEB_TAVILY_API_KEYS"`
	BaseURL    string   `json:"base_url"    env:"CLAW_TOOLS_WEB_TAVILY_BASE_URL"`
	MaxResults int      `json:"max_results" env:"CLAW_TOOLS_WEB_TAVILY_MAX_RESULTS"`
}

type DuckDuckGoConfig struct {
	Enabled    bool `json:"enabled"     env:"CLAW_TOOLS_WEB_DUCKDUCKGO_ENABLED"`
	MaxResults int  `json:"max_results" env:"CLAW_TOOLS_WEB_DUCKDUCKGO_MAX_RESULTS"`
}

type PerplexityConfig struct {
	Enabled    bool     `json:"enabled"     env:"CLAW_TOOLS_WEB_PERPLEXITY_ENABLED"`
	APIKey     string   `json:"api_key"     env:"CLAW_TOOLS_WEB_PERPLEXITY_API_KEY"`
	APIKeys    []string `json:"api_keys"    env:"CLAW_TOOLS_WEB_PERPLEXITY_API_KEYS"`
	MaxResults int      `json:"max_results" env:"CLAW_TOOLS_WEB_PERPLEXITY_MAX_RESULTS"`
}

type SearXNGConfig struct {
	Enabled    bool   `json:"enabled"     env:"CLAW_TOOLS_WEB_SEARXNG_ENABLED"`
	BaseURL    string `json:"base_url"    env:"CLAW_TOOLS_WEB_SEARXNG_BASE_URL"`
	MaxResults int    `json:"max_results" env:"CLAW_TOOLS_WEB_SEARXNG_MAX_RESULTS"`
}

type GLMSearchConfig struct {
	Enabled bool   `json:"enabled"  env:"CLAW_TOOLS_WEB_GLM_ENABLED"`
	APIKey  string `json:"api_key"  env:"CLAW_TOOLS_WEB_GLM_API_KEY"`
	BaseURL string `json:"base_url" env:"CLAW_TOOLS_WEB_GLM_BASE_URL"`
	// SearchEngine specifies the search backend: "search_std" (default),
	// "search_pro", "search_pro_sogou", or "search_pro_quark".
	SearchEngine string `json:"search_engine" env:"CLAW_TOOLS_WEB_GLM_SEARCH_ENGINE"`
	MaxResults   int    `json:"max_results"   env:"CLAW_TOOLS_WEB_GLM_MAX_RESULTS"`
}

type WebToolsConfig struct {
	ToolConfig `                 envPrefix:"CLAW_TOOLS_WEB_"`
	Brave      BraveConfig      `                                json:"brave"`
	Tavily     TavilyConfig     `                                json:"tavily"`
	DuckDuckGo DuckDuckGoConfig `                                json:"duckduckgo"`
	Perplexity PerplexityConfig `                                json:"perplexity"`
	SearXNG    SearXNGConfig    `                                json:"searxng"`
	GLMSearch  GLMSearchConfig  `                                json:"glm_search"`
	// Proxy is an optional proxy URL for web tools (http/https/socks5/socks5h).
	// For authenticated proxies, prefer HTTP_PROXY/HTTPS_PROXY env vars instead of embedding credentials in config.
	Proxy           string `json:"proxy,omitempty"             env:"CLAW_TOOLS_WEB_PROXY"`
	FetchLimitBytes int64  `json:"fetch_limit_bytes,omitempty" env:"CLAW_TOOLS_WEB_FETCH_LIMIT_BYTES"`
}

type CronToolsConfig struct {
	ToolConfig         `    envPrefix:"CLAW_TOOLS_CRON_"`
	ExecTimeoutMinutes int `                                 env:"CLAW_TOOLS_CRON_EXEC_TIMEOUT_MINUTES" json:"exec_timeout_minutes"` // 0 means no timeout
}

type ExecConfig struct {
	ToolConfig          `         envPrefix:"CLAW_TOOLS_EXEC_"`
	EnableDenyPatterns  bool     `                                 env:"CLAW_TOOLS_EXEC_ENABLE_DENY_PATTERNS"  json:"enable_deny_patterns"`
	CustomDenyPatterns  []string `                                 env:"CLAW_TOOLS_EXEC_CUSTOM_DENY_PATTERNS"  json:"custom_deny_patterns"`
	CustomAllowPatterns []string `                                 env:"CLAW_TOOLS_EXEC_CUSTOM_ALLOW_PATTERNS" json:"custom_allow_patterns"`
	TimeoutSeconds      int      `                                 env:"CLAW_TOOLS_EXEC_TIMEOUT_SECONDS"       json:"timeout_seconds"` // 0 means use default (60s)
}

type SkillsToolsConfig struct {
	Local                 ToolConfig             `json:"local"                    envPrefix:"CLAW_TOOLS_SKILLS_LOCAL_"`
	Registry              ToolConfig             `json:"registry"                 envPrefix:"CLAW_TOOLS_SKILLS_REGISTRY_"`
	Registries            SkillsRegistriesConfig `json:"registries"`
	Github                SkillsGithubConfig     `json:"github"`
	MaxConcurrentSearches int                    `json:"max_concurrent_searches"  env:"CLAW_TOOLS_SKILLS_MAX_CONCURRENT_SEARCHES"`
	SearchCache           SearchCacheConfig      `json:"search_cache"`
}

type MediaCleanupConfig struct {
	ToolConfig `    envPrefix:"CLAW_MEDIA_CLEANUP_"`
	MaxAge     int `                                    env:"CLAW_MEDIA_CLEANUP_MAX_AGE"  json:"max_age_minutes"`
	Interval   int `                                    env:"CLAW_MEDIA_CLEANUP_INTERVAL" json:"interval_minutes"`
}

type ReadFileToolConfig struct {
	Enabled         bool `json:"enabled"`
	MaxReadFileSize int  `json:"max_read_file_size"`
}

type ToolsConfig struct {
	AllowReadPaths  []string `json:"allow_read_paths"  env:"CLAW_TOOLS_ALLOW_READ_PATHS"`
	AllowWritePaths []string `json:"allow_write_paths" env:"CLAW_TOOLS_ALLOW_WRITE_PATHS"`
	// Overrides is a generic per-tool enable map keyed by published tool name
	// (e.g. "skill_find"). It is the dynamic gating path for tools that have no
	// dedicated typed field — namespaced/global-layer tools register here so the
	// WebUI can toggle them without code changes. Checked first by IsToolEnabled.
	Overrides    map[string]bool    `json:"tool_overrides,omitempty"`
	Web          WebToolsConfig     `json:"web"`
	Cron         CronToolsConfig    `json:"cron"`
	Exec         ExecConfig         `json:"exec"`
	Skills       SkillsToolsConfig  `json:"skills"`
	MediaCleanup MediaCleanupConfig `json:"media_cleanup"`
	MCP          MCPConfig          `json:"mcp"`
	// Discovery holds progressive-tool-discovery settings. It applies to all tool
	// kinds (native, suites, MCP), so it lives at tools.discovery rather than under
	// tools.mcp.
	Discovery ToolDiscoveryConfig `json:"discovery"`
	// ReadFile carries the read-size limit used at tool construction (its enabled
	// state, like every per-tool toggle, lives in Overrides now).
	ReadFile ReadFileToolConfig `json:"read_file"                                                envPrefix:"CLAW_TOOLS_READ_FILE_"`
	// Subagent is a capability gate (off by default), not a per-tool enable.
	Subagent ToolConfig `json:"subagent"                                                 envPrefix:"CLAW_TOOLS_SUBAGENT_"`
}

type SearchCacheConfig struct {
	MaxSize    int `json:"max_size"    env:"CLAW_SKILLS_SEARCH_CACHE_MAX_SIZE"`
	TTLSeconds int `json:"ttl_seconds" env:"CLAW_SKILLS_SEARCH_CACHE_TTL_SECONDS"`
}

type SkillsRegistriesConfig struct {
	ClawHub ClawHubRegistryConfig `json:"clawhub"`
}

type SkillsGithubConfig struct {
	Token string `json:"token,omitempty" env:"CLAW_TOOLS_SKILLS_GITHUB_AUTH_TOKEN"`
	Proxy string `json:"proxy,omitempty" env:"CLAW_TOOLS_SKILLS_GITHUB_PROXY"`
}

type ClawHubRegistryConfig struct {
	Enabled         bool   `json:"enabled"           env:"CLAW_SKILLS_REGISTRIES_CLAWHUB_ENABLED"`
	BaseURL         string `json:"base_url"          env:"CLAW_SKILLS_REGISTRIES_CLAWHUB_BASE_URL"`
	AuthToken       string `json:"auth_token"        env:"CLAW_SKILLS_REGISTRIES_CLAWHUB_AUTH_TOKEN"`
	SearchPath      string `json:"search_path"       env:"CLAW_SKILLS_REGISTRIES_CLAWHUB_SEARCH_PATH"`
	SkillsPath      string `json:"skills_path"       env:"CLAW_SKILLS_REGISTRIES_CLAWHUB_SKILLS_PATH"`
	DownloadPath    string `json:"download_path"     env:"CLAW_SKILLS_REGISTRIES_CLAWHUB_DOWNLOAD_PATH"`
	Timeout         int    `json:"timeout"           env:"CLAW_SKILLS_REGISTRIES_CLAWHUB_TIMEOUT"`
	MaxZipSize      int    `json:"max_zip_size"      env:"CLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_ZIP_SIZE"`
	MaxResponseSize int    `json:"max_response_size" env:"CLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_RESPONSE_SIZE"`
}

// MCPServerConfig defines configuration for a single MCP server
type MCPServerConfig struct {
	// Enabled indicates whether this MCP server is active
	Enabled bool `json:"enabled"`
	// Command is the executable to run (e.g., "npx", "python", "/path/to/server")
	Command string `json:"command"`
	// Args are the arguments to pass to the command
	Args []string `json:"args,omitempty"`
	// Env are environment variables to set for the server process (stdio only)
	Env map[string]string `json:"env,omitempty"`
	// EnvFile is the path to a file containing environment variables (stdio only)
	EnvFile string `json:"env_file,omitempty"`
	// Type is "stdio", "sse", or "http" (default: stdio if command is set, sse if url is set)
	Type string `json:"type,omitempty"`
	// URL is used for SSE/HTTP transport
	URL string `json:"url,omitempty"`
	// Headers are HTTP headers to send with requests (sse/http only)
	Headers map[string]string `json:"headers,omitempty"`
	// RevealTogether, under progressive discovery, reveals all of this server's
	// tools as soon as one is discovered, so a small cohesive server is unlocked in
	// a single search instead of tool-by-tool. Ignored when discovery is off.
	RevealTogether bool `json:"reveal_together,omitempty"`
}

// DefaultMCPLivenessProbeSeconds is the default interval of the per-server
// tools/list probe. One small request a minute per server detects a dead
// session and picks up a changed tool list within the interval, which matters
// because a server built on mcp-go cannot push tools/list_changed to us.
const DefaultMCPLivenessProbeSeconds = 60

// MCPConfig defines configuration for all MCP servers
type MCPConfig struct {
	// Servers is a map of server name to server configuration
	Servers map[string]MCPServerConfig `json:"servers,omitempty"`
	// ReconnectCooldownSeconds is the wait applied to a server after a failed
	// connect, before another attempt is made for it; it doubles per further
	// failure up to 10 minutes and resets on success. Prevents hammering a dead
	// upstream on every call. 0 uses the default (30s).
	ReconnectCooldownSeconds int `json:"reconnect_cooldown_seconds,omitempty"`
	// CallTimeoutSeconds is a backstop deadline applied to a tool call only when the
	// caller's context carries no deadline, so a hung server cannot block forever.
	// 0 uses the default (300s).
	CallTimeoutSeconds int `json:"call_timeout_seconds,omitempty"`
	// LivenessProbeSeconds is the interval of the periodic tools/list probe per
	// connected server: a failed probe reconnects that server so the next real
	// call finds a live session, and a changed answer refreshes the server's
	// tools. Defaults to DefaultMCPLivenessProbeSeconds; 0 disables probing. Not
	// omitempty, so an explicit 0 survives a save and is not read back as the
	// default.
	LivenessProbeSeconds int `json:"liveness_probe_seconds"`
}

// MCPClientEffectivelyEnabled reports whether claw should connect out to
// external MCP servers: true iff at least one configured server is enabled.
func (t *ToolsConfig) MCPClientEffectivelyEnabled() bool {
	for _, s := range t.MCP.Servers {
		if s.Enabled {
			return true
		}
	}
	return false
}

// MCPHostConfig defines configuration for the MCP server claw exposes
// (claw acting as an MCP server), used by CLI providers (claude-cli,
// codex-cli, antigravity-cli, cursor-cli) so they can call claw's host-side tools natively
// instead of emitting tool-call JSON in their prose. The allowlist is
// global — applied once for all CLI clients, not per-LLM.
type MCPHostConfig struct {
	Enabled bool `json:"enabled"                     env:"CLAW_MCP_HOST_ENABLED"`
	// AutoEnable, when true, starts the MCP host automatically whenever any
	// enabled model in ModelList uses a *-cli protocol (claude-cli, codex-cli,
	// antigravity-cli,
	// gemini-cli). Those CLIs depend on MCP to call claw's host-side tools.
	// Explicit Enabled=true always wins.
	AutoEnable   bool   `json:"auto_enable"             env:"CLAW_MCP_HOST_AUTO_ENABLE"`
	Listen       string `json:"listen,omitempty"        env:"CLAW_MCP_HOST_LISTEN"`
	EndpointPath string `json:"endpoint_path,omitempty" env:"CLAW_MCP_HOST_ENDPOINT_PATH"`
	// InternalTools and ExternalTools are per-endpoint visibility filters that
	// govern which tools appear in tools/list (the catalogue) on /internal and
	// the bearer endpoint (/mcp) respectively. They are a COARSE exposure filter
	// — per-agent execution gating still applies on top at tools/call. Each entry
	// is matched by MatchVisibility: equality-or-prefix after collapsing
	// underscores and stripping a leading mcp_, so "file"/"session_info" catch
	// local tools and "fusion"/"fusion_wxca" catch upstream MCP tools (no mcp_
	// prefix or glob needed). "*" exposes everything; empty exposes nothing.
	InternalTools []string `json:"internal_tools,omitempty"`
	ExternalTools []string `json:"external_tools,omitempty"`
}

// AlwaysShownNamespaces returns the discovery-eligible tool namespaces pinned to
// stay visible to the in-loop model under progressive discovery. Nil-safe.
func (c *Config) AlwaysShownNamespaces() []string {
	if c == nil {
		return nil
	}
	return c.Tools.Discovery.AlwaysShownNamespaces
}

// MCPHostEffectivelyEnabled returns true when the MCP host should run.
// Explicit Enabled=true always starts it. When Enabled=false but
// AutoEnable=true, it starts iff a CLI provider is configured.
func (c *Config) MCPHostEffectivelyEnabled() bool {
	if c.MCPHost.Enabled {
		return true
	}
	return c.MCPHost.AutoEnable && c.HasCLIProvider()
}

// IsToolEnabled is ToolEnabled for a caller without a per-tool default: the
// capability gates "mcp" and "subagent" default to their settings, any other
// tool to enabled.
func (t *ToolsConfig) IsToolEnabled(name string) bool {
	// Capability gates (off by default; these are not per-tool enables).
	// Callers that lack a per-tool default treat any other tool as enabled
	// unless an override disables it.
	defaultAllow := true
	switch name {
	case "mcp":
		defaultAllow = t.MCPClientEffectivelyEnabled()
	case "subagent":
		defaultAllow = t.Subagent.Enabled
	}
	return t.ToolEnabled(name, defaultAllow)
}

// ToolEnabled resolves a per-tool enabled state: an explicit Overrides entry wins,
// otherwise the tool's own default-allow (from its descriptor) applies. This is the
// gating path for global-layer tools, which have no dedicated typed config field.
//
// shell_exec is the exception: it has no install-wide switch, so it is always
// enabled here and any tool_overrides entry for it is ignored; whether an agent
// gets it is decided by AgentConfig.IsToolAllowed alone.
func (t *ToolsConfig) ToolEnabled(name string, defaultAllow bool) bool {
	// shell_exec has no install-wide switch: the agent's tools list decides.
	if IsShellTool(name) {
		return true
	}
	if v, ok := t.Overrides[name]; ok {
		return v
	}
	return defaultAllow
}
