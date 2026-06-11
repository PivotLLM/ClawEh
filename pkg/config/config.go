package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/PivotLLM/ClawEh/pkg/fileutil"
	"github.com/PivotLLM/ClawEh/pkg/global"
)

// rrCounter is a global counter for round-robin load balancing across models.
var rrCounter atomic.Uint64

// FlexibleStringSlice is a []string that also accepts JSON numbers,
// so allow_from can contain both "123" and 123.
// It also supports parsing comma-separated strings from environment variables,
// including both English (,) and Chinese (，) commas.
type FlexibleStringSlice []string

func (f *FlexibleStringSlice) UnmarshalJSON(data []byte) error {
	// Try []string first
	var ss []string
	if err := json.Unmarshal(data, &ss); err == nil {
		*f = ss
		return nil
	}

	// Try []interface{} to handle mixed types
	var raw []any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	result := make([]string, 0, len(raw))
	for _, v := range raw {
		switch val := v.(type) {
		case string:
			result = append(result, val)
		case float64:
			result = append(result, fmt.Sprintf("%.0f", val))
		default:
			result = append(result, fmt.Sprintf("%v", val))
		}
	}
	*f = result
	return nil
}

// UnmarshalText implements encoding.TextUnmarshaler to support env variable parsing.
// It handles comma-separated values with both English (,) and Chinese (，) commas.
func (f *FlexibleStringSlice) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*f = nil
		return nil
	}

	s := string(text)
	// Replace Chinese comma with English comma, then split
	s = strings.ReplaceAll(s, "，", ",")
	parts := strings.Split(s, ",")

	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	*f = result
	return nil
}

// AgentMentionConfig controls how agent names are extracted from message content.
type AgentMentionConfig struct {
	// Triggers is the set of prefix characters that introduce an agent mention.
	// Defaults to ["@", "/", "."] when empty.
	Triggers []string `json:"triggers,omitempty"`
}

// SecurityConfig holds security-related configuration options.
type SecurityConfig struct {
	// CallbackPrefix is prepended to all messages received via the callback
	// endpoint before they reach the LLM. When empty, the global
	// DefaultCallbackPrefix constant is used.
	CallbackPrefix string `json:"callback_prefix,omitempty"`
}

type Config struct {
	Agents        AgentsConfig        `json:"agents"`
	Bindings      []AgentBinding      `json:"bindings,omitempty"`
	Session       SessionConfig       `json:"session,omitempty"`
	AgentMentions AgentMentionConfig  `json:"agent_mentions,omitempty"`
	Channels      ChannelsConfig      `json:"channels"`
	Providers     ProvidersConfig     `json:"providers,omitempty"`
	ModelList     []ModelConfig       `json:"model_list"` // New model-centric provider configuration
	Summarization SummarizationConfig `json:"summarization,omitempty"`
	Gateway       GatewayConfig       `json:"gateway"`
	Tools         ToolsConfig         `json:"tools"`
	Devices       DevicesConfig       `json:"devices"`
	Voice         VoiceConfig         `json:"voice"`
	Logging       LoggingConfig       `json:"logging"`
	Security      SecurityConfig      `json:"security,omitempty"`
	MCPHost       MCPHostConfig       `json:"mcp_host,omitempty"`
	// ConfigReloadIntervalSeconds controls how often the daemon polls the config
	// file for changes and triggers a reload. Defaults to
	// global.DefaultConfigReloadIntervalSeconds; floored at
	// global.MinConfigReloadIntervalSeconds.
	ConfigReloadIntervalSeconds int `json:"config_reload_interval_seconds,omitempty" env:"CLAW_CONFIG_RELOAD_INTERVAL_SECONDS"`

	// OpenAICompatProtocols registers additional protocol prefixes that should
	// be served by the openai-compatible HTTP provider. Keys are the protocol
	// identifier (the part before "/" in a ModelConfig.Model string); values
	// are the default api_base for that protocol — empty means "no default,
	// each model must set api_base explicitly". Keys must not collide with
	// any protocol owned by a hardcoded provider (see reservedProtocolNames).
	OpenAICompatProtocols map[string]string `json:"openai_compat_protocols,omitempty"`

	// OpenAICompatResponseFormat overrides the per-protocol default for
	// emitting `response_format={"type":"json_object"}` on outbound requests
	// when the caller asks for JSON-mode output. Built-in defaults: openai
	// and xai are capable; everything else is off. Operators can flip a
	// configurable protocol on (e.g. openrouter, groq, litellm) without
	// touching code by setting `openai_compat_response_format: {openrouter:
	// true}` here. Protocols not present in the map fall back to the
	// hardcoded default.
	OpenAICompatResponseFormat map[string]bool `json:"openai_compat_response_format,omitempty"`

	dataDir string // runtime-only: base data directory, not serialized
}

// MarshalJSON implements custom JSON marshaling for Config
// to omit providers section when empty and session when empty
func (c Config) MarshalJSON() ([]byte, error) {
	type Alias Config
	aux := &struct {
		Providers *ProvidersConfig `json:"providers,omitempty"`
		Session   *SessionConfig   `json:"session,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(&c),
	}

	// Only include providers if not empty
	if !c.Providers.IsEmpty() {
		aux.Providers = &c.Providers
	}

	// Only include session if not empty
	if c.Session.Mode != "" || len(c.Session.IdentityLinks) > 0 {
		aux.Session = &c.Session
	}

	return json.Marshal(aux)
}

type AgentsConfig struct {
	Defaults AgentDefaults `json:"defaults"`
	List     []AgentConfig `json:"list,omitempty"`
}

// AgentModelConfig supports both string and structured model config.
// String format: "gpt-4" (just primary, no fallbacks)
// Object format: {"primary": "gpt-4", "fallbacks": ["claude-haiku"]}
type AgentModelConfig struct {
	Primary   string   `json:"primary,omitempty"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

func (m *AgentModelConfig) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		m.Primary = s
		m.Fallbacks = nil
		return nil
	}
	type raw struct {
		Primary   string   `json:"primary"`
		Fallbacks []string `json:"fallbacks"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	m.Primary = r.Primary
	m.Fallbacks = r.Fallbacks
	return nil
}

func (m AgentModelConfig) MarshalJSON() ([]byte, error) {
	type raw struct {
		Primary   string   `json:"primary,omitempty"`
		Fallbacks []string `json:"fallbacks,omitempty"`
	}
	return json.Marshal(raw{Primary: m.Primary, Fallbacks: m.Fallbacks})
}

// SummarizationConfig is the global, deployment-wide summarization model chain.
// Models are tried in order for context compaction across all agents; each entry
// is a model_list alias (or a raw protocol/model string). The agent's own
// primary model is always appended as a final last-resort fallback at runtime.
// An empty Models list means summarization runs against each agent's own model.
type SummarizationConfig struct {
	Models []string `json:"models,omitempty"`
	// DebugCapture, when true, appends the verbatim request and response of every
	// summarization LLM invocation to <agent-workspace>/compact.jsonl. Debugging
	// only; off by default.
	DebugCapture bool `json:"debug_capture,omitempty" env:"CLAW_SUMMARIZATION_DEBUG_CAPTURE"`
}

type AgentConfig struct {
	ID        string `json:"id"`
	Enabled   *bool  `json:"enabled,omitempty"`
	Default   bool   `json:"default,omitempty"`
	Name      string `json:"name,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	// MemoryDir relocates this agent's memory folder off the default
	// <workspace>/memory location. Empty preserves the default. Non-empty
	// must be an absolute path (may contain ~). The on-disk path is hidden
	// from the agent; the system prompt continues to advertise the canonical
	// <workspace>/memory/... paths and the read/write/list/edit/append tools
	// transparently redirect "memory/..." accesses to this folder.
	MemoryDir   string            `json:"memory_dir,omitempty"`
	Model       *AgentModelConfig `json:"model,omitempty"`
	Skills      []string          `json:"skills,omitempty"`
	Tools       []string          `json:"tools,omitempty"`
	Subagents   *SubagentsConfig  `json:"subagents,omitempty"`
	Callback    *CallbackConfig   `json:"callback,omitempty"`
	Temperature *float64          `json:"temperature,omitempty"`

	CompressMinPercent         *int     `json:"compress_min_percent,omitempty"`
	CompressNormalPercent      *int     `json:"compress_normal_percent,omitempty"`
	CompressSafetyPercent      *int     `json:"compress_safety_percent,omitempty"`
	CompressMessageThreshold   *int     `json:"compress_message_threshold,omitempty"`
	CompressRetainTokenPercent *int     `json:"compress_retain_token_percent,omitempty"`
	CompressRetainMinMessages  *int     `json:"compress_retain_min_messages,omitempty"`
	CompressCharsPerToken      *float64 `json:"compress_chars_per_token,omitempty"`
	CompressTokenSafetyMargin  *float64 `json:"compress_token_safety_margin,omitempty"`
	ArchiveMessageCount        *int     `json:"archive_message_count,omitempty"`
	ArchiveDays                *int     `json:"archive_days,omitempty"`
	SummaryMaxCount            *int     `json:"summary_max_count,omitempty"`
	SummaryRetentionDays       *int     `json:"summary_retention_days,omitempty"`
	ArchiveContentMaxBytes     *int     `json:"archive_content_max_bytes,omitempty"`
}

// IsEnabled returns true if the agent is enabled (nil means enabled by default).
func (a *AgentConfig) IsEnabled() bool {
	return a.Enabled == nil || *a.Enabled
}

// MatchToolPattern returns true if name matches any entry in patterns.
// "*" matches anything. Entries ending in "*" are case-insensitive prefix
// matches. Other entries are case-insensitive exact matches. An empty
// patterns slice matches nothing.
func MatchToolPattern(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return false
	}
	lowerName := strings.ToLower(name)
	for _, entry := range patterns {
		if entry == "*" {
			return true
		}
		if strings.HasSuffix(entry, "*") {
			prefix := strings.ToLower(strings.TrimSuffix(entry, "*"))
			if strings.HasPrefix(lowerName, prefix) {
				return true
			}
		} else if strings.EqualFold(entry, name) {
			return true
		}
	}
	return false
}

// IsToolAllowed returns true if the named tool is permitted for this agent.
// A nil or empty Tools list denies all tools. Use ["*"] to allow all tools.
// Entries ending in "*" are treated as case-insensitive prefix matches.
// Exact entries are matched as-is.
func (a *AgentConfig) IsToolAllowed(name string) bool {
	if a == nil {
		return false
	}
	// nil Tools (key absent in config) → use install defaults.
	// Empty Tools (tools: [] in config) → deny all intentionally.
	if a.Tools == nil {
		return MatchToolPattern(DefaultAgentTools, name)
	}
	return MatchToolPattern(a.Tools, name)
}

type SubagentsConfig struct {
	AllowAgents []string          `json:"allow_agents,omitempty"`
	Model       *AgentModelConfig `json:"model,omitempty"`
}

// CallbackConfig controls the rotating-token callback system for an agent.
// WindowMinutes==0 (or omitted) disables callbacks entirely.
type CallbackConfig struct {
	WindowMinutes int `json:"window_minutes"`
	WindowCount   int `json:"window_count"`
}

type PeerMatch struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type BindingMatch struct {
	Channel   string     `json:"channel"`
	AccountID string     `json:"account_id,omitempty"`
	Peer      *PeerMatch `json:"peer,omitempty"`
	GuildID   string     `json:"guild_id,omitempty"`
	TeamID    string     `json:"team_id,omitempty"`
}

type AgentBinding struct {
	AgentID       string       `json:"agent_id"`
	AgentMentions []string     `json:"agent_mentions,omitempty"`
	Match         BindingMatch `json:"match"`
}

type SessionConfig struct {
	Mode          string              `json:"mode,omitempty"`
	IdentityLinks map[string][]string `json:"identity_links,omitempty"`
}

// RoutingConfig controls the intelligent model routing feature.
// When enabled, each incoming message is scored against structural features
// (message length, code blocks, tool call history, conversation depth, attachments).
// Messages scoring below Threshold are sent to LightModel; all others use the
// agent's primary model. This reduces cost and latency for simple tasks without
// requiring any keyword matching — all scoring is language-agnostic.
type RoutingConfig struct {
	Enabled    bool    `json:"enabled"`
	LightModel string  `json:"light_model"` // model_name from model_list to use for simple tasks
	Threshold  float64 `json:"threshold"`   // complexity score in [0,1]; score >= threshold → primary model
}

type AgentDefaults struct {
	Workspace           string `json:"workspace,omitempty"             env:"CLAW_AGENTS_DEFAULTS_WORKSPACE"`
	RestrictToWorkspace bool   `json:"restrict_to_workspace"           env:"CLAW_AGENTS_DEFAULTS_RESTRICT_TO_WORKSPACE"`
	// StreamToolActivity, when true, sends the model's inter-tool narration and
	// each tool's user-facing output to the channel as it happens. When false
	// (default) the user receives only the final answer, not the play-by-play.
	StreamToolActivity         bool              `json:"stream_tool_activity,omitempty"  env:"CLAW_AGENTS_DEFAULTS_STREAM_TOOL_ACTIVITY"`
	AllowReadOutsideWorkspace  bool              `json:"allow_read_outside_workspace"    env:"CLAW_AGENTS_DEFAULTS_ALLOW_READ_OUTSIDE_WORKSPACE"`
	Model                      *AgentModelConfig `json:"model,omitempty"`
	ImageModel                 string            `json:"image_model,omitempty"           env:"CLAW_AGENTS_DEFAULTS_IMAGE_MODEL"`
	ImageModelFallbacks        []string          `json:"image_model_fallbacks,omitempty"`
	RequestTimeout             int               `json:"request_timeout,omitempty"       env:"CLAW_AGENTS_DEFAULTS_REQUEST_TIMEOUT"`
	MaxTokens                  int               `json:"max_tokens"                      env:"CLAW_AGENTS_DEFAULTS_MAX_TOKENS"`
	Temperature                *float64          `json:"temperature,omitempty"           env:"CLAW_AGENTS_DEFAULTS_TEMPERATURE"`
	MaxToolIterations          int               `json:"max_tool_iterations"             env:"CLAW_AGENTS_DEFAULTS_MAX_TOOL_ITERATIONS"`
	ContextWindow              int               `json:"context_window,omitempty"        env:"CLAW_AGENTS_DEFAULTS_CONTEXT_WINDOW"`
	MaxMediaSize               int               `json:"max_media_size,omitempty"        env:"CLAW_AGENTS_DEFAULTS_MAX_MEDIA_SIZE"`
	CompressMinPercent         int               `json:"compress_min_percent,omitempty"          env:"CLAW_AGENTS_DEFAULTS_COMPRESS_MIN_PERCENT"`
	CompressNormalPercent      int               `json:"compress_normal_percent,omitempty"       env:"CLAW_AGENTS_DEFAULTS_COMPRESS_NORMAL_PERCENT"`
	CompressSafetyPercent      int               `json:"compress_safety_percent,omitempty"       env:"CLAW_AGENTS_DEFAULTS_COMPRESS_SAFETY_PERCENT"`
	CompressMessageThreshold   int               `json:"compress_message_threshold,omitempty"    env:"CLAW_AGENTS_DEFAULTS_COMPRESS_MESSAGE_THRESHOLD"`
	CompressRetainTokenPercent int               `json:"compress_retain_token_percent,omitempty" env:"CLAW_AGENTS_DEFAULTS_COMPRESS_RETAIN_TOKEN_PERCENT"`
	CompressRetainMinMessages  int               `json:"compress_retain_min_messages,omitempty"  env:"CLAW_AGENTS_DEFAULTS_COMPRESS_RETAIN_MIN_MESSAGES"`
	CompressCharsPerToken      float64           `json:"compress_chars_per_token,omitempty"      env:"CLAW_AGENTS_DEFAULTS_COMPRESS_CHARS_PER_TOKEN"`
	CompressTokenSafetyMargin  float64           `json:"compress_token_safety_margin,omitempty"  env:"CLAW_AGENTS_DEFAULTS_COMPRESS_TOKEN_SAFETY_MARGIN"`
	ArchiveMessageCount        int               `json:"archive_message_count,omitempty"         env:"CLAW_AGENTS_DEFAULTS_ARCHIVE_MESSAGE_COUNT"`
	ArchiveDays                int               `json:"archive_days,omitempty"                  env:"CLAW_AGENTS_DEFAULTS_ARCHIVE_DAYS"`
	SummaryMaxCount            int               `json:"summary_max_count,omitempty"             env:"CLAW_AGENTS_DEFAULTS_SUMMARY_MAX_COUNT"`
	SummaryRetentionDays       int               `json:"summary_retention_days,omitempty"        env:"CLAW_AGENTS_DEFAULTS_SUMMARY_RETENTION_DAYS"`
	ArchiveContentMaxBytes     int               `json:"archive_content_max_bytes,omitempty"     env:"CLAW_AGENTS_DEFAULTS_ARCHIVE_CONTENT_MAX_BYTES"`
	DefaultTools               []string          `json:"default_tools,omitempty"`
	Routing                    *RoutingConfig    `json:"routing,omitempty"`
}

const DefaultMaxMediaSize = 20 * 1024 * 1024 // 20 MB

func (d *AgentDefaults) GetMaxMediaSize() int {
	if d.MaxMediaSize > 0 {
		return d.MaxMediaSize
	}
	return DefaultMaxMediaSize
}

// DefaultModelName returns the primary model name, or "" if unset.
func (d *AgentDefaults) DefaultModelName() string {
	if d.Model == nil {
		return ""
	}
	return d.Model.Primary
}

// SetDefaultModel sets the primary model, preserving any existing fallbacks.
func (d *AgentDefaults) SetDefaultModel(modelName string) {
	if d.Model == nil {
		d.Model = &AgentModelConfig{Primary: modelName}
	} else {
		d.Model.Primary = modelName
	}
}

type ChannelsConfig struct {
	Telegram []TelegramBotConfig `json:"telegram"`
	Discord  DiscordConfig       `json:"discord"`
	Slack    SlackConfig         `json:"slack"`
	Matrix   MatrixConfig        `json:"matrix"`
	LINE     LINEConfig          `json:"line"`
	WebUI    WebUIConfig         `json:"webui"`
}

// GroupTriggerConfig controls when the bot responds in group chats.
type GroupTriggerConfig struct {
	MentionOnly bool     `json:"mention_only,omitempty"`
	Prefixes    []string `json:"prefixes,omitempty"`
}

// TypingConfig controls typing indicator behavior (Phase 10).
type TypingConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// PlaceholderConfig controls placeholder message behavior (Phase 10).
type PlaceholderConfig struct {
	Enabled bool   `json:"enabled,omitempty"`
	Text    string `json:"text,omitempty"`
}

// CoalesceConfig controls inbound message coalescing. When a client (notably
// the Telegram app) splits a single long paste into several messages, those
// arrive as separate updates. Coalescing buffers consecutive messages from the
// same sender in the same chat and combines them into one inbound message once
// no new message has arrived for WindowMS, so the agent processes them as a
// single turn instead of one round (and reply) per fragment.
type CoalesceConfig struct {
	// Enabled gates coalescing. It is a pointer so that an absent value means
	// "on" (the default): a nil Enabled enables coalescing, so existing bot
	// configs that predate this field get it without editing. Set it explicitly
	// to false to disable. See IsEnabled.
	Enabled *bool `json:"enabled,omitempty"`
	// WindowMS is the quiet period (milliseconds) to wait after the most recent
	// message before flushing the buffer. Each new message resets the timer.
	// Zero falls back to DefaultCoalesceWindowMS.
	WindowMS int `json:"window_ms,omitempty"`
	// MaxMessages caps how many messages a single buffer may accumulate before
	// it is flushed regardless of the timer. Zero falls back to
	// DefaultCoalesceMaxMessages.
	MaxMessages int `json:"max_messages,omitempty"`
	// MaxWaitMS caps the total time a buffer may stay open from its first
	// message, even if messages keep resetting the window timer. Zero falls
	// back to DefaultCoalesceMaxWaitMS.
	MaxWaitMS int `json:"max_wait_ms,omitempty"`
}

// Coalesce defaults applied when a field is left at its zero value.
const (
	DefaultCoalesceWindowMS    = 1000
	DefaultCoalesceMaxMessages = 50
	DefaultCoalesceMaxWaitMS   = 30000
)

// Window returns the configured quiet period as a duration, applying the
// default when unset.
func (c CoalesceConfig) Window() time.Duration {
	ms := c.WindowMS
	if ms <= 0 {
		ms = DefaultCoalesceWindowMS
	}
	return time.Duration(ms) * time.Millisecond
}

// MaxWait returns the configured maximum buffer lifetime as a duration,
// applying the default when unset.
func (c CoalesceConfig) MaxWait() time.Duration {
	ms := c.MaxWaitMS
	if ms <= 0 {
		ms = DefaultCoalesceMaxWaitMS
	}
	return time.Duration(ms) * time.Millisecond
}

// MaxMessageCount returns the configured maximum buffered message count,
// applying the default when unset.
func (c CoalesceConfig) MaxMessageCount() int {
	if c.MaxMessages <= 0 {
		return DefaultCoalesceMaxMessages
	}
	return c.MaxMessages
}

// IsEnabled reports whether coalescing is active. A nil Enabled (the field
// absent from config) defaults to on, so bots that predate the field — and
// freshly configured bots — coalesce by default. An explicit false disables it.
func (c CoalesceConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// TelegramBotConfig defines a single named Telegram bot.
// Each entry creates a separate channel named "telegram-<id>", except when id is
// empty or "default" which creates the standard "telegram" channel.
type TelegramBotConfig struct {
	ID                 string              `json:"id"`
	Enabled            bool                `json:"enabled"`
	Token              string              `json:"token"`
	BaseURL            string              `json:"base_url,omitempty"`
	Proxy              string              `json:"proxy,omitempty"`
	AllowFrom          FlexibleStringSlice `json:"allow_from,omitempty"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	Coalesce           CoalesceConfig      `json:"coalesce,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id,omitempty"`
}

// ChannelName returns the channel identifier for this bot.
// Bots with an empty or "default" ID use "telegram".
// All other IDs produce "telegram-<id>".
func (b TelegramBotConfig) ChannelName() string {
	if b.ID == "" || b.ID == "default" {
		return "telegram"
	}
	return "telegram-" + b.ID
}

type DiscordConfig struct {
	Enabled            bool                `json:"enabled"                 env:"CLAW_CHANNELS_DISCORD_ENABLED"`
	Token              string              `json:"token"                   env:"CLAW_CHANNELS_DISCORD_TOKEN"`
	Proxy              string              `json:"proxy"                   env:"CLAW_CHANNELS_DISCORD_PROXY"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"CLAW_CHANNELS_DISCORD_ALLOW_FROM"`
	MentionOnly        bool                `json:"mention_only"            env:"CLAW_CHANNELS_DISCORD_MENTION_ONLY"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"CLAW_CHANNELS_DISCORD_REASONING_CHANNEL_ID"`
}

type SlackConfig struct {
	Enabled            bool                `json:"enabled"                 env:"CLAW_CHANNELS_SLACK_ENABLED"`
	BotToken           string              `json:"bot_token"               env:"CLAW_CHANNELS_SLACK_BOT_TOKEN"`
	AppToken           string              `json:"app_token"               env:"CLAW_CHANNELS_SLACK_APP_TOKEN"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"CLAW_CHANNELS_SLACK_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"CLAW_CHANNELS_SLACK_REASONING_CHANNEL_ID"`
}

type MatrixConfig struct {
	Enabled            bool                `json:"enabled"                  env:"CLAW_CHANNELS_MATRIX_ENABLED"`
	Homeserver         string              `json:"homeserver"               env:"CLAW_CHANNELS_MATRIX_HOMESERVER"`
	UserID             string              `json:"user_id"                  env:"CLAW_CHANNELS_MATRIX_USER_ID"`
	AccessToken        string              `json:"access_token"             env:"CLAW_CHANNELS_MATRIX_ACCESS_TOKEN"`
	DeviceID           string              `json:"device_id,omitempty"      env:"CLAW_CHANNELS_MATRIX_DEVICE_ID"`
	JoinOnInvite       bool                `json:"join_on_invite"           env:"CLAW_CHANNELS_MATRIX_JOIN_ON_INVITE"`
	MessageFormat      string              `json:"message_format,omitempty" env:"CLAW_CHANNELS_MATRIX_MESSAGE_FORMAT"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"               env:"CLAW_CHANNELS_MATRIX_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"     env:"CLAW_CHANNELS_MATRIX_REASONING_CHANNEL_ID"`
}

type LINEConfig struct {
	Enabled            bool                `json:"enabled"                 env:"CLAW_CHANNELS_LINE_ENABLED"`
	ChannelSecret      string              `json:"channel_secret"          env:"CLAW_CHANNELS_LINE_CHANNEL_SECRET"`
	ChannelAccessToken string              `json:"channel_access_token"    env:"CLAW_CHANNELS_LINE_CHANNEL_ACCESS_TOKEN"`
	WebhookHost        string              `json:"webhook_host"            env:"CLAW_CHANNELS_LINE_WEBHOOK_HOST"`
	WebhookPort        int                 `json:"webhook_port"            env:"CLAW_CHANNELS_LINE_WEBHOOK_PORT"`
	WebhookPath        string              `json:"webhook_path"            env:"CLAW_CHANNELS_LINE_WEBHOOK_PATH"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"CLAW_CHANNELS_LINE_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"CLAW_CHANNELS_LINE_REASONING_CHANNEL_ID"`
}

type WebUIConfig struct {
	Enabled         bool                `json:"enabled"                     env:"CLAW_CHANNELS_WEBUI_ENABLED"`
	Token           string              `json:"token"                       env:"CLAW_CHANNELS_WEBUI_TOKEN"`
	AllowTokenQuery bool                `json:"allow_token_query,omitempty"`
	AllowOrigins    []string            `json:"allow_origins,omitempty"`
	PingInterval    int                 `json:"ping_interval,omitempty"`
	ReadTimeout     int                 `json:"read_timeout,omitempty"`
	WriteTimeout    int                 `json:"write_timeout,omitempty"`
	MaxConnections  int                 `json:"max_connections,omitempty"`
	AllowFrom       FlexibleStringSlice `json:"allow_from"                  env:"CLAW_CHANNELS_WEBUI_ALLOW_FROM"`
	Placeholder     PlaceholderConfig   `json:"placeholder,omitempty"`
}

type DevicesConfig struct {
	Enabled    bool `json:"enabled"     env:"CLAW_DEVICES_ENABLED"`
	MonitorUSB bool `json:"monitor_usb" env:"CLAW_DEVICES_MONITOR_USB"`
}

type VoiceConfig struct {
	EchoTranscription bool `json:"echo_transcription" env:"CLAW_VOICE_ECHO_TRANSCRIPTION"`
}

type LoggingConfig struct {
	File    bool   `json:"file"                env:"CLAW_LOGGING_FILE"`
	Console bool   `json:"console"             env:"CLAW_LOGGING_CONSOLE"`
	Level   string `json:"level"               env:"CLAW_LOGGING_LEVEL"`
	JSON    bool   `json:"json"                env:"CLAW_LOGGING_JSON"`
	// LogMessageContent controls whether inbound message text and API request/response
	// bodies are included in log entries. Defaults to false to protect user privacy.
	LogMessageContent bool `json:"log_message_content" env:"CLAW_LOGGING_MESSAGE_CONTENT"`
	// DumpRefusals, when true, writes the full LLM input and output to a file
	// in logs/dumps/ whenever the provider returns finish_reason "refusal".
	DumpRefusals bool `json:"dump_refusals" env:"CLAW_LOGGING_DUMP_REFUSALS"`
	// DumpAll, when true, writes the full LLM input and output to a file
	// in logs/dumps/ for every LLM response, regardless of finish reason.
	DumpAll bool `json:"dump_all" env:"CLAW_LOGGING_DUMP_ALL"`
}

type ProvidersConfig struct {
	Anthropic  ProviderConfig       `json:"anthropic"`
	OpenAI     OpenAIProviderConfig `json:"openai"`
	LiteLLM    ProviderConfig       `json:"litellm"`
	OpenRouter ProviderConfig       `json:"openrouter"`
	Groq       ProviderConfig       `json:"groq"`
	VLLM       ProviderConfig       `json:"vllm"`
	Gemini     ProviderConfig       `json:"gemini"`
	Nvidia     ProviderConfig       `json:"nvidia"`
	Ollama     ProviderConfig       `json:"ollama"`
	Moonshot   ProviderConfig       `json:"moonshot"`
	DeepSeek   ProviderConfig       `json:"deepseek"`
	Cerebras   ProviderConfig       `json:"cerebras"`
	Qwen       ProviderConfig       `json:"qwen"`
	Mistral    ProviderConfig       `json:"mistral"`
	Avian      ProviderConfig       `json:"avian"`
}

// IsEmpty checks if all provider configs are empty (no API keys or API bases set)
// Note: WebSearch is an optimization option and doesn't count as "non-empty"
func (p ProvidersConfig) IsEmpty() bool {
	return p.Anthropic.APIKey == "" && p.Anthropic.APIBase == "" &&
		p.OpenAI.APIKey == "" && p.OpenAI.APIBase == "" &&
		p.LiteLLM.APIKey == "" && p.LiteLLM.APIBase == "" &&
		p.OpenRouter.APIKey == "" && p.OpenRouter.APIBase == "" &&
		p.Groq.APIKey == "" && p.Groq.APIBase == "" &&
		p.VLLM.APIKey == "" && p.VLLM.APIBase == "" &&
		p.Gemini.APIKey == "" && p.Gemini.APIBase == "" &&
		p.Nvidia.APIKey == "" && p.Nvidia.APIBase == "" &&
		p.Ollama.APIKey == "" && p.Ollama.APIBase == "" &&
		p.Moonshot.APIKey == "" && p.Moonshot.APIBase == "" &&
		p.DeepSeek.APIKey == "" && p.DeepSeek.APIBase == "" &&
		p.Cerebras.APIKey == "" && p.Cerebras.APIBase == "" &&
		p.Qwen.APIKey == "" && p.Qwen.APIBase == "" &&
		p.Mistral.APIKey == "" && p.Mistral.APIBase == "" &&
		p.Avian.APIKey == "" && p.Avian.APIBase == ""
}

// MarshalJSON implements custom JSON marshaling for ProvidersConfig
// to omit the entire section when empty
func (p ProvidersConfig) MarshalJSON() ([]byte, error) {
	if p.IsEmpty() {
		return []byte("null"), nil
	}
	type Alias ProvidersConfig
	return json.Marshal((*Alias)(&p))
}

type ProviderConfig struct {
	APIKey         string `json:"api_key"                   env:"CLAW_PROVIDERS_{{.Name}}_API_KEY"`
	APIBase        string `json:"api_base"                  env:"CLAW_PROVIDERS_{{.Name}}_API_BASE"`
	Proxy          string `json:"proxy,omitempty"           env:"CLAW_PROVIDERS_{{.Name}}_PROXY"`
	RequestTimeout int    `json:"request_timeout,omitempty" env:"CLAW_PROVIDERS_{{.Name}}_REQUEST_TIMEOUT"`
	AuthMethod     string `json:"auth_method,omitempty"     env:"CLAW_PROVIDERS_{{.Name}}_AUTH_METHOD"`
	ConnectMode    string `json:"connect_mode,omitempty"    env:"CLAW_PROVIDERS_{{.Name}}_CONNECT_MODE"`
}

type OpenAIProviderConfig struct {
	ProviderConfig
	WebSearch bool `json:"web_search" env:"CLAW_PROVIDERS_OPENAI_WEB_SEARCH"`
}

// ModelConfig represents a model-centric provider configuration.
// It allows adding new providers (especially OpenAI-compatible ones) via configuration only.
// The model field uses protocol prefix format: [protocol/]model-identifier
// Supported protocols: openai, anthropic, claude-cli, codex-cli
// Default protocol is "openai" if no prefix is specified.
type ModelConfig struct {
	// Required fields
	ModelName string `json:"model_name"` // User-facing alias for the model
	Model     string `json:"model"`      // Protocol/model-identifier (e.g., "openai/gpt-4o", "anthropic/claude-sonnet-4.6")

	// HTTP-based providers
	APIBase string `json:"api_base,omitempty"` // API endpoint URL
	APIKey  string `json:"api_key"`            // API authentication key
	Proxy   string `json:"proxy,omitempty"`    // HTTP proxy URL

	// Special providers (CLI-based, OAuth, etc.)
	AuthMethod  string `json:"auth_method,omitempty"`  // Authentication method: oauth, token
	ConnectMode string `json:"connect_mode,omitempty"` // Connection mode: stdio, grpc
	Workspace   string `json:"workspace,omitempty"`    // Workspace path for CLI-based providers
	Command     string `json:"command,omitempty"`      // Override binary path for CLI providers (e.g., /home/user/.local/bin/claude)

	// Optional optimizations
	RPM            int               `json:"rpm,omitempty"`              // Requests per minute limit
	MaxTokens      int               `json:"max_tokens,omitempty"`       // Maximum tokens per response; overrides agent defaults
	ContextWindow  int               `json:"context_window,omitempty"`   // Actual model context window size in tokens
	MaxTokensField string            `json:"max_tokens_field,omitempty"` // Field name for max tokens (e.g., "max_completion_tokens")
	RequestTimeout int               `json:"request_timeout,omitempty"`
	StrictCompat   bool              `json:"strict_compat,omitempty"`  // Strip non-standard fields for strict OpenAI-compatible endpoints
	ThinkingLevel  string            `json:"thinking_level,omitempty"` // Extended thinking: off|low|medium|high|xhigh|adaptive
	NoTools        bool              `json:"no_tools,omitempty"`       // When true, tools are not passed to this model
	ExtraArgs      []string          `json:"extra_args,omitempty"`     // Additional CLI arguments appended after required flags
	Env            map[string]string `json:"env,omitempty"`            // Environment variables for CLI-based providers (merged with os.Environ)
	Enabled        bool              `json:"enabled"`                  // If false, model is skipped in all operations

	// ResponseLogFile, when non-empty, causes every raw HTTP response body from
	// the openai_compat provider to be appended to the given path. Diagnostic
	// feature only; no rotation, no expansion of ~ or env vars. Ignored by
	// providers other than openai_compat.
	ResponseLogFile string `json:"response_log_file,omitempty"`

	// ReasoningEffort sets the OpenAI-style reasoning_effort request field for
	// models that natively accept it (notably Grok). Valid values are "low",
	// "medium", "high", or empty (the field is omitted). Providers that don't
	// understand the field will silently ignore it.
	ReasoningEffort string `json:"reasoning_effort,omitempty" yaml:"reasoning_effort,omitempty"`

	// ExtraBody is a free-form passthrough map merged into the JSON request
	// body for OpenAI-compatible providers. Use it for per-provider knobs that
	// claw does not model natively. Keys colliding with claw-managed fields
	// (see reservedRequestBodyKeys) are rejected at config load.
	ExtraBody map[string]any `json:"extra_body,omitempty" yaml:"extra_body,omitempty"`

	// DropParams lists top-level request-body fields to strip before sending to
	// OpenAI-compatible providers. Use it to suppress a parameter a model or
	// upstream rejects — e.g. "temperature" for OpenRouter reasoning models that
	// don't advertise it and would 404 under provider.require_parameters.
	// Stripping is applied last (after extra_body), so it always wins. It is a
	// literal filter: listing structural fields like "messages" or "model" will
	// break the request. Ignored by providers other than openai_compat.
	DropParams []string `json:"drop_params,omitempty" yaml:"drop_params,omitempty"`

	// Runtime-only: set by Config.resolveOpenAICompatProtocols when the
	// model's protocol prefix matches an entry in
	// Config.OpenAICompatProtocols. Unexported so JSON ignores them.
	openaiCompatExtra bool
	openaiCompatBase  string

	// Runtime-only: set during config resolution from
	// Config.OpenAICompatResponseFormat. When true, the openai-compat
	// provider built from this ModelConfig is allowed to emit
	// response_format=json_object on outbound requests (the built-in
	// per-protocol default is OR'd in by the provider itself). Unexported
	// so JSON ignores it.
	responseFormatJSON bool
}

// IsOpenAICompatExtra reports whether this model's protocol prefix was
// registered via Config.OpenAICompatProtocols. The providers factory uses
// this to route an otherwise-unknown protocol through the openai-compat
// HTTP provider.
func (c *ModelConfig) IsOpenAICompatExtra() bool {
	if c == nil {
		return false
	}
	return c.openaiCompatExtra
}

// OpenAICompatBase returns the default api_base registered for this model's
// protocol via Config.OpenAICompatProtocols. Empty when the protocol was not
// registered, or when it was registered with an empty default.
func (c *ModelConfig) OpenAICompatBase() string {
	if c == nil {
		return ""
	}
	return c.openaiCompatBase
}

// MarkOpenAICompatExtra tags this ModelConfig as resolved via
// Config.OpenAICompatProtocols. base is the registered default api_base
// (may be empty). Called by Config.resolveOpenAICompatProtocols; also useful
// in tests that construct a ModelConfig directly without going through
// LoadConfig.
func (c *ModelConfig) MarkOpenAICompatExtra(base string) {
	c.openaiCompatExtra = true
	c.openaiCompatBase = base
}

// ResponseFormatJSONCapable reports whether this ModelConfig has been
// resolved as capable of receiving response_format=json_object via the
// Config.OpenAICompatResponseFormat override. The built-in per-protocol
// defaults are applied separately inside the openai-compat provider; this
// flag only conveys the operator-supplied override.
func (c *ModelConfig) ResponseFormatJSONCapable() bool {
	if c == nil {
		return false
	}
	return c.responseFormatJSON
}

// SetResponseFormatJSONCapable records the operator-supplied override that
// the openai-compat provider should be permitted to emit response_format=
// json_object for this model. Called by config resolution; exposed so tests
// can build ModelConfigs directly without round-tripping through LoadConfig.
func (c *ModelConfig) SetResponseFormatJSONCapable(v bool) {
	c.responseFormatJSON = v
}

// reservedRequestBodyKeys lists the JSON request fields owned by claw's own
// request builder. ExtraBody entries colliding with these keys are rejected at
// config load — the collision guard there is what guarantees the merge step in
// the request builder cannot overwrite a claw-managed field.
var reservedRequestBodyKeys = map[string]struct{}{
	"model":                 {},
	"messages":              {},
	"stream":                {},
	"tools":                 {},
	"tool_choice":           {},
	"parallel_tool_calls":   {},
	"reasoning_effort":      {},
	"temperature":           {},
	"max_tokens":            {},
	"max_completion_tokens": {},
	"top_p":                 {},
	"n":                     {},
}

// Validate checks if the ModelConfig has all required fields.
func (c *ModelConfig) Validate() error {
	if c.ModelName == "" {
		return fmt.Errorf("model_name is required")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required")
	}
	switch c.ReasoningEffort {
	case "", "low", "medium", "high":
		// ok
	default:
		return fmt.Errorf(
			"model %q: invalid reasoning_effort %q (valid: low, medium, high, or omit)",
			c.ModelName, c.ReasoningEffort,
		)
	}
	for k := range c.ExtraBody {
		if _, reserved := reservedRequestBodyKeys[k]; reserved {
			return fmt.Errorf(
				"model %q: extra_body key %q collides with claw-managed request field",
				c.ModelName, k,
			)
		}
	}
	return nil
}

type GatewayConfig struct {
	Host string `json:"host" env:"CLAW_GATEWAY_HOST"`
	Port int    `json:"port" env:"CLAW_GATEWAY_PORT"`
}

type ToolDiscoveryConfig struct {
	Enabled          bool `json:"enabled"            env:"CLAW_TOOLS_DISCOVERY_ENABLED"`
	TTL              int  `json:"ttl"                env:"CLAW_TOOLS_DISCOVERY_TTL"`
	MaxSearchResults int  `json:"max_search_results" env:"CLAW_MAX_SEARCH_RESULTS"`
	UseBM25          bool `json:"use_bm25"           env:"CLAW_TOOLS_DISCOVERY_USE_BM25"`
	UseRegex         bool `json:"use_regex"          env:"CLAW_TOOLS_DISCOVERY_USE_REGEX"`
}

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
	AllowRemote         bool     `                                 env:"CLAW_TOOLS_EXEC_ALLOW_REMOTE"          json:"allow_remote"`
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
}

// MCPConfig defines configuration for all MCP servers
type MCPConfig struct {
	ToolConfig `                    envPrefix:"CLAW_TOOLS_MCP_"`
	Discovery  ToolDiscoveryConfig `                                json:"discovery"`
	// Servers is a map of server name to server configuration
	Servers map[string]MCPServerConfig `json:"servers,omitempty"`
}

// MCPHostConfig defines configuration for the MCP server claw exposes
// (claw acting as an MCP server), used by CLI providers (claude-cli,
// codex-cli, gemini-cli) so they can call claw's host-side tools natively
// instead of emitting tool-call JSON in their prose. The allowlist is
// global — applied once for all CLI clients, not per-LLM.
type MCPHostConfig struct {
	Enabled bool `json:"enabled"                     env:"CLAW_MCP_HOST_ENABLED"`
	// AutoEnable, when true, starts the MCP host automatically whenever any
	// enabled model in ModelList uses a *-cli protocol (claude-cli, codex-cli,
	// gemini-cli). Those CLIs depend on MCP to call claw's host-side tools.
	// Explicit Enabled=true always wins.
	AutoEnable   bool   `json:"auto_enable"             env:"CLAW_MCP_HOST_AUTO_ENABLE"`
	Listen       string `json:"listen,omitempty"        env:"CLAW_MCP_HOST_LISTEN"`
	EndpointPath string `json:"endpoint_path,omitempty" env:"CLAW_MCP_HOST_ENDPOINT_PATH"`
	// Tools is the global allowlist of tool names exposed to MCP clients.
	// Supports "*" (all), prefix globs like "read_*", and exact names.
	// The agent's outbound "message" tool is never exposed.
	Tools []string `json:"tools,omitempty"`
}

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	// Pre-scan the JSON to check how many model_list / agents.list entries the
	// user provided. Go's JSON decoder reuses existing slice backing-array
	// elements rather than zero-initializing them, so fields absent from the
	// user's JSON (e.g. workspace) would silently inherit values from the
	// DefaultConfig template at the same index position. Zero out each slice
	// before the real unmarshal when the user provides their own entries; keep
	// the built-in defaults only when the user provides none.
	var tmp Config
	if err := json.Unmarshal(data, &tmp); err != nil {
		return nil, err
	}
	if len(tmp.ModelList) > 0 {
		cfg.ModelList = nil
	}
	if len(tmp.Agents.List) > 0 {
		cfg.Agents.List = nil
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	warnLegacyCompressModel(data)

	if err := env.Parse(cfg); err != nil {
		return nil, err
	}

	// Migrate legacy channel config fields to new unified structures
	cfg.migrateChannelConfigs()

	// Auto-migrate: if only legacy providers config exists, convert to model_list
	if len(cfg.ModelList) == 0 && cfg.HasProvidersConfig() {
		cfg.ModelList = ConvertProvidersToModelList(cfg)
	}

	// Validate model_list for uniqueness and required fields
	if err := cfg.ValidateModelList(); err != nil {
		return nil, err
	}

	// Reject openai_compat_protocols entries that collide with hardcoded
	// providers, then tag matching model_list entries so the providers
	// factory can route them via the openai-compat HTTP provider.
	if err := cfg.ValidateOpenAICompatProtocols(); err != nil {
		return nil, err
	}
	cfg.resolveOpenAICompatProtocols()

	return cfg, nil
}

// warnLegacyCompressModel logs a one-line warning when the loaded config still
// carries the removed per-agent `compress_model` field. Summarization models are
// now configured globally via the top-level `summarization.models` list.
func warnLegacyCompressModel(data []byte) {
	var legacy struct {
		Agents struct {
			Defaults struct {
				CompressModel json.RawMessage `json:"compress_model"`
			} `json:"defaults"`
			List []struct {
				ID            string          `json:"id"`
				CompressModel json.RawMessage `json:"compress_model"`
			} `json:"list"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return
	}
	if len(legacy.Agents.Defaults.CompressModel) > 0 {
		log.Printf("config: ignoring removed field agents.defaults.compress_model; configure summarization models globally via summarization.models")
	}
	for _, a := range legacy.Agents.List {
		if len(a.CompressModel) > 0 {
			log.Printf("config: ignoring removed field compress_model on agent %q; configure summarization models globally via summarization.models", a.ID)
		}
	}
}

func (c *Config) migrateChannelConfigs() {
	// Discord: mention_only -> group_trigger.mention_only
	if c.Channels.Discord.MentionOnly && !c.Channels.Discord.GroupTrigger.MentionOnly {
		c.Channels.Discord.GroupTrigger.MentionOnly = true
	}
}

func SaveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	// Use unified atomic write utility with explicit sync for flash storage reliability.
	return fileutil.WriteFileAtomic(path, data, 0o600)
}

// HasCLIProvider reports whether any enabled model in ModelList uses a
// *-cli protocol (claude-cli, codex-cli, gemini-cli). Those CLIs rely on
// the MCP host to call claw's tools natively.
func (c *Config) HasCLIProvider() bool {
	for i := range c.ModelList {
		if !c.ModelList[i].Enabled {
			continue
		}
		model := strings.TrimSpace(c.ModelList[i].Model)
		protocol, _, found := strings.Cut(model, "/")
		if !found {
			continue
		}
		switch protocol {
		case "claude-cli", "claudecli",
			"codex-cli", "codexcli",
			"gemini-cli", "geminicli":
			return true
		}
	}
	return false
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

// ConfigReloadInterval returns the effective config-file polling interval as a
// time.Duration. Falls back to global.DefaultConfigReloadIntervalSeconds when
// unset or negative, and clamps to global.MinConfigReloadIntervalSeconds.
func (c *Config) ConfigReloadInterval() time.Duration {
	secs := c.ConfigReloadIntervalSeconds
	if secs <= 0 {
		secs = global.DefaultConfigReloadIntervalSeconds
	}
	if secs < global.MinConfigReloadIntervalSeconds {
		secs = global.MinConfigReloadIntervalSeconds
	}
	return time.Duration(secs) * time.Second
}

func (c *Config) WorkspacePath() string {
	if c.Agents.Defaults.Workspace != "" {
		return expandHome(c.Agents.Defaults.Workspace)
	}
	return filepath.Join(c.dataDir, "agents", "default")
}

// AgentSessionDirs returns the sessions subdirectory for every configured
// agent, deduped. This mirrors the workspace resolution logic in
// pkg/agent/instance.go:resolveAgentWorkspace. The result is used by the
// WebUI to enumerate sessions across all agents, not just the defaults workspace.
func (c *Config) AgentSessionDirs() []string {
	defaultWS := c.WorkspacePath() // agents/default (or custom)
	agentsDir := filepath.Dir(defaultWS)

	seen := make(map[string]struct{})
	var dirs []string
	add := func(ws string) {
		d := filepath.Join(ws, "sessions")
		if _, dup := seen[d]; !dup {
			seen[d] = struct{}{}
			dirs = append(dirs, d)
		}
	}

	for _, ac := range c.Agents.List {
		if !ac.IsEnabled() {
			continue
		}
		if ws := strings.TrimSpace(ac.Workspace); ws != "" {
			add(expandHome(ws))
			continue
		}
		id := strings.ToLower(strings.TrimSpace(ac.ID))
		if id == "" || id == "main" {
			add(defaultWS)
		} else {
			add(filepath.Join(agentsDir, id))
		}
	}

	// Always include the defaults workspace — covers agents that were removed
	// from config but left files on disk.
	add(defaultWS)

	return dirs
}

// DataDir returns the base data directory (~/.claw or $CLAW_HOME).
func (c *Config) DataDir() string {
	return c.dataDir
}

// SkillsPath returns the centralized skills directory (~/.claw/skills).
func (c *Config) SkillsPath() string {
	return filepath.Join(c.dataDir, "skills")
}

// CronPath returns the cron store directory (~/.claw/cron).
func (c *Config) CronPath() string {
	return filepath.Join(c.dataDir, "cron")
}

func (c *Config) GetAPIKey() string {
	if c.Providers.OpenRouter.APIKey != "" {
		return c.Providers.OpenRouter.APIKey
	}
	if c.Providers.Anthropic.APIKey != "" {
		return c.Providers.Anthropic.APIKey
	}
	if c.Providers.OpenAI.APIKey != "" {
		return c.Providers.OpenAI.APIKey
	}
	if c.Providers.Gemini.APIKey != "" {
		return c.Providers.Gemini.APIKey
	}
	if c.Providers.Groq.APIKey != "" {
		return c.Providers.Groq.APIKey
	}
	if c.Providers.VLLM.APIKey != "" {
		return c.Providers.VLLM.APIKey
	}
	if c.Providers.Cerebras.APIKey != "" {
		return c.Providers.Cerebras.APIKey
	}
	return ""
}

func (c *Config) GetAPIBase() string {
	if c.Providers.OpenRouter.APIKey != "" {
		if c.Providers.OpenRouter.APIBase != "" {
			return c.Providers.OpenRouter.APIBase
		}
		return "https://openrouter.ai/api/v1"
	}
	if c.Providers.VLLM.APIKey != "" && c.Providers.VLLM.APIBase != "" {
		return c.Providers.VLLM.APIBase
	}
	return ""
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(path) > 1 && path[1] == '/' {
			return home + path[1:]
		}
		return home
	}
	return path
}

// GetModelConfig returns the ModelConfig for the given model name.
// If multiple configs exist with the same model_name, it uses round-robin
// selection for load balancing. Returns an error if the model is not found.
func (c *Config) GetModelConfig(modelName string) (*ModelConfig, error) {
	matches := c.findMatches(modelName)
	if len(matches) == 0 {
		return nil, fmt.Errorf("model %q not found in model_list or providers", modelName)
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}

	// Multiple configs - use round-robin for load balancing
	idx := rrCounter.Add(1) % uint64(len(matches))
	return &matches[idx], nil
}

// findMatches finds all ModelConfig entries with the given model_name.
func (c *Config) findMatches(modelName string) []ModelConfig {
	var matches []ModelConfig
	for i := range c.ModelList {
		if c.ModelList[i].ModelName == modelName && c.ModelList[i].Enabled {
			matches = append(matches, c.ModelList[i])
		}
	}
	return matches
}

// HasProvidersConfig checks if any provider in the old providers config has configuration.
func (c *Config) HasProvidersConfig() bool {
	return !c.Providers.IsEmpty()
}

// ValidateModelList validates all ModelConfig entries in the model_list.
// It checks that each model config is valid.
// Note: Multiple entries with the same model_name are allowed for load balancing.
func (c *Config) ValidateModelList() error {
	for i := range c.ModelList {
		if err := c.ModelList[i].Validate(); err != nil {
			return fmt.Errorf("model_list[%d]: %w", i, err)
		}
	}
	return nil
}

// reservedProtocolNames is the set of protocol identifiers owned by a
// hardcoded case branch in pkg/providers/factory_provider.go. Entries in
// Config.OpenAICompatProtocols may not use these names — operators should
// rely on the built-in behavior for these protocols, or pick a different
// identifier.
//
// Keep this list in sync with the switch in
// pkg/providers/factory_provider.go:CreateProviderFromConfig.
var reservedProtocolNames = map[string]struct{}{
	"openai":             {},
	"azure":              {},
	"azure-openai":       {},
	"bedrock":            {},
	"litellm":            {},
	"openrouter":         {},
	"groq":               {},
	"gemini":             {},
	"nvidia":             {},
	"ollama":             {},
	"moonshot":           {},
	"deepseek":           {},
	"cerebras":           {},
	"vllm":               {},
	"qwen":               {},
	"mistral":            {},
	"avian":              {},
	"xai":                {},
	"anthropic":          {},
	"anthropic-messages": {},
	"claude-cli":         {},
	"claudecli":          {},
	"codex-cli":          {},
	"codexcli":           {},
	"gemini-cli":         {},
	"geminicli":          {},
}

// IsReservedProtocol reports whether name collides with a protocol owned by
// a hardcoded provider in pkg/providers/factory_provider.go.
func IsReservedProtocol(name string) bool {
	_, ok := reservedProtocolNames[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// ValidateOpenAICompatProtocols checks that no entry in OpenAICompatProtocols
// collides with a hardcoded provider protocol or is otherwise malformed.
// An entry duplicating a hardcoded openai-compat protocol (e.g. "xai") is
// rejected — those protocols already ship with the right defaults, and
// allowing operators to silently override them would mask drift between the
// config-driven default and the hardcoded one.
func (c *Config) ValidateOpenAICompatProtocols() error {
	for proto := range c.OpenAICompatProtocols {
		p := strings.TrimSpace(proto)
		if p == "" {
			return fmt.Errorf("openai_compat_protocols: empty protocol name")
		}
		if strings.ContainsAny(p, "/ \t") {
			return fmt.Errorf("openai_compat_protocols: protocol name %q must not contain '/' or whitespace", proto)
		}
		if IsReservedProtocol(p) {
			return fmt.Errorf(
				"openai_compat_protocols: protocol %q is reserved by a built-in provider; remove the entry or pick a different name",
				proto,
			)
		}
	}
	return nil
}

// resolveOpenAICompatProtocols walks ModelList and tags any entry whose
// protocol prefix appears in OpenAICompatProtocols, recording the registered
// default api_base on the entry so the providers factory can route it through
// the openai-compat HTTP provider without a hardcoded switch entry. It also
// applies any per-protocol overrides from OpenAICompatResponseFormat so the
// openai-compat provider knows whether it may emit response_format=json_object
// when a caller asks for JSON-mode output.
func (c *Config) resolveOpenAICompatProtocols() {
	for i := range c.ModelList {
		proto := extractProtocolStatic(c.ModelList[i].Model)
		if len(c.OpenAICompatProtocols) > 0 {
			if base, ok := c.OpenAICompatProtocols[proto]; ok {
				c.ModelList[i].MarkOpenAICompatExtra(base)
			}
		}
		if enabled, ok := c.OpenAICompatResponseFormat[proto]; ok {
			c.ModelList[i].SetResponseFormatJSONCapable(enabled)
		}
	}
}

// extractProtocolStatic mirrors providers.ExtractProtocol; duplicated here to
// avoid an import cycle (providers imports config).
func extractProtocolStatic(model string) string {
	model = strings.TrimSpace(model)
	protocol, _, found := strings.Cut(model, "/")
	if !found {
		return "openai"
	}
	return protocol
}

func MergeAPIKeys(apiKey string, apiKeys []string) []string {
	seen := make(map[string]struct{})
	var all []string

	if k := strings.TrimSpace(apiKey); k != "" {
		if _, exists := seen[k]; !exists {
			seen[k] = struct{}{}
			all = append(all, k)
		}
	}

	for _, k := range apiKeys {
		if trimmed := strings.TrimSpace(k); trimmed != "" {
			if _, exists := seen[trimmed]; !exists {
				seen[trimmed] = struct{}{}
				all = append(all, trimmed)
			}
		}
	}

	return all
}

func (t *ToolsConfig) IsToolEnabled(name string) bool {
	// Generic overrides win — this is the dynamic gating path for global-layer
	// tools that have no dedicated typed field.
	if v, ok := t.Overrides[name]; ok {
		return v
	}
	switch name {
	// Capability gates (off by default; these are not per-tool enables).
	case "mcp":
		return t.MCP.Enabled
	case "subagent":
		return t.Subagent.Enabled
	default:
		// Capability gates aside, callers that lack a per-tool default treat a
		// tool as enabled unless an override disables it.
		return true
	}
}

// ToolEnabled resolves a per-tool enabled state: an explicit Overrides entry wins,
// otherwise the tool's own default-allow (from its descriptor) applies. This is the
// gating path for global-layer tools, which have no dedicated typed config field.
func (t *ToolsConfig) ToolEnabled(name string, defaultAllow bool) bool {
	if v, ok := t.Overrides[name]; ok {
		return v
	}
	return defaultAllow
}
