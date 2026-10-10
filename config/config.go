package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
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

// SecurityConfig holds security-related configuration options.
type SecurityConfig struct {
	// MessagePrefix is prepended to all messages received via the external-message
	// endpoint before they reach the LLM. When empty, the global
	// DefaultMessagePrefix constant is used.
	MessagePrefix string `json:"message_prefix,omitempty"`
}

type Config struct {
	Agents        AgentsConfig        `json:"agents"`
	Bindings      []AgentBinding      `json:"bindings,omitempty"`
	Session       SessionConfig       `json:"session,omitempty"`
	AgentMentions AgentMentionConfig  `json:"agent_mentions,omitempty"`
	Channels      ChannelsConfig      `json:"channels"`
	Providers     []Provider          `json:"providers"`
	Models        []ModelConfig       `json:"models"` // Models, each reached through a named provider
	Summarization SummarizationConfig `json:"summarization,omitempty"`
	Gateway       GatewayConfig       `json:"gateway"`
	Tools         ToolsConfig         `json:"tools"`
	Devices       DevicesConfig       `json:"devices"`
	Voice         VoiceConfig         `json:"voice"`
	Logging       LoggingConfig       `json:"logging"`
	Security      SecurityConfig      `json:"security,omitempty"`
	MCPHost       MCPHostConfig       `json:"mcp_host,omitempty"`
	Cooldown      CooldownConfig      `json:"cooldown,omitempty"`
	Backup        BackupConfig        `json:"backup,omitempty"`
	Forum         ForumConfig         `json:"forum,omitzero"`
	// DefaultConfig marks a never-saved, auto-seeded config. DefaultConfig() sets
	// it true and SeedDefaultConfig() preserves it on disk; the first save through
	// SaveConfig clears it. The setup wizard uses it (with a "no usable model"
	// guard) to detect a fresh install and offer onboarding. NOT omitempty: the
	// false value must be written explicitly, or LoadConfig (which bases off
	// DefaultConfig()=true) would re-inherit true for a saved config.
	DefaultConfig bool `json:"default_config"`
	// ConfigReloadIntervalSeconds controls how often the daemon polls the config
	// file for changes and triggers a reload. Defaults to
	// global.DefaultConfigReloadIntervalSeconds; floored at
	// global.MinConfigReloadIntervalSeconds.
	ConfigReloadIntervalSeconds int `json:"config_reload_interval_seconds,omitempty" env:"CLAW_CONFIG_RELOAD_INTERVAL_SECONDS"`

	dataDir string // runtime-only: base data directory, not serialized

	// secretRefs records every "env:"/"file:" secret reference this config was
	// loaded with, so a save writes the reference back rather than the value it
	// resolved to. Runtime-only; see secrets.go.
	secretRefs []secretRef
}

// MarshalJSON implements custom JSON marshaling for Config to omit the session
// section when empty, and to write an empty providers or models list as [],
// never null or absent: LoadConfig fills a list whose key is absent with the
// built-in defaults, so an operator who deleted every entry must have that
// choice written down.
func (c *Config) MarshalJSON() ([]byte, error) {
	type Alias Config
	aux := &struct {
		Session   *SessionConfig `json:"session,omitempty"`
		Providers []Provider     `json:"providers"`
		Models    []ModelConfig  `json:"models"`
		*Alias
	}{
		Providers: nonNilSlice(c.Providers),
		Models:    nonNilSlice(c.Models),
		Alias:     (*Alias)(c),
	}

	// Only include session if not empty
	if c.Session.RetentionDays > 0 {
		aux.Session = &c.Session
	}

	return json.Marshal(aux)
}

// nonNilSlice returns s, or an empty slice when s is nil, so it encodes as []
// rather than null.
func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// warnLegacyMaestroBool logs one warning per agent whose config still uses the
// retired `"maestro": true|false` form. Such agents run without Maestro until
// the block is set (for example from the WebUI).
func warnLegacyMaestroBool(cfg *Config) {
	for i := range cfg.Agents.List {
		a := &cfg.Agents.List[i]
		if a.Maestro.LegacyBoolean() {
			logger.WarnCF("config", "agent uses the retired boolean \"maestro\" setting; Maestro is disabled for it until re-enabled as {\"enabled\": true}",
				map[string]any{"agent": a.ID})
		}
	}
}

// warnShellOverride logs a warning when tools.tool_overrides still sets
// shell_exec. There is no install-wide shell switch: an agent runs shell
// commands only when its own tools list names shell_exec.
func warnShellOverride(cfg *Config) {
	if _, ok := cfg.Tools.Overrides[ShellExecTool]; ok {
		logger.WarnCF("config", "tools.tool_overrides.shell_exec has no effect; allow shell commands per agent by adding shell_exec to its tools",
			map[string]any{"key": "tools.tool_overrides." + ShellExecTool})
	}
}

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

// CooldownConfig sets, per HTTP-status category, how long a model that keeps
// failing is taken out of rotation (the "settled" cooldown reached after the
// short 1/3/5-minute escalation on the first three consecutive failures). Each
// value is in MINUTES: 0 uses the built-in default; a negative value disables
// cooldown for that category (the model is never taken out for it). 413
// (context-too-large) and errors with no HTTP status never cool — they are
// per-request or transient.
type CooldownConfig struct {
	// BillingAuthMinutes covers HTTP 401, 402, 403 (auth / out-of-credits).
	BillingAuthMinutes int `json:"billing_auth_minutes,omitempty" env:"CLAW_COOLDOWN_BILLING_AUTH_MINUTES"`
	// RateLimitMinutes covers HTTP 429.
	RateLimitMinutes int `json:"rate_limit_minutes,omitempty" env:"CLAW_COOLDOWN_RATE_LIMIT_MINUTES"`
	// BadRequestMinutes covers HTTP 400.
	BadRequestMinutes int `json:"bad_request_minutes,omitempty" env:"CLAW_COOLDOWN_BAD_REQUEST_MINUTES"`
	// ClientErrorMinutes covers other 4xx (404, 408, …; not 400/401/402/403/429/413).
	ClientErrorMinutes int `json:"client_error_minutes,omitempty" env:"CLAW_COOLDOWN_CLIENT_ERROR_MINUTES"`
	// ServerErrorMinutes covers 5xx.
	ServerErrorMinutes int `json:"server_error_minutes,omitempty" env:"CLAW_COOLDOWN_SERVER_ERROR_MINUTES"`
}

// Cooldown category defaults (minutes). Billing/auth is the longest because the
// operator usually has to top up or rotate a key, but it is still bounded at 30
// minutes: it doubles as the recheck interval, so a longer value means a
// resolved billing problem goes unnoticed for that much longer. A rejected 402
// costs a round trip and no inference, so probing more often is cheap.
const (
	DefaultCooldownBillingAuthMinutes = 30
	DefaultCooldownRateLimitMinutes   = 10
	// DefaultCooldownBadRequestMinutes is 0 (never cool): a bad request is a
	// request-shape rejection, so the fallback should try the next candidate
	// (including a sibling config of the same provider+model, e.g. thinking-off)
	// instead of parking the model.
	DefaultCooldownBadRequestMinutes  = 0
	DefaultCooldownClientErrorMinutes = 10
	DefaultCooldownServerErrorMinutes = 10
)

// minutesOrDefault maps a config value to a duration: 0 → def, <0 → 0 (disabled).
func minutesOrDefault(v, def int) time.Duration {
	if v < 0 {
		return 0
	}
	if v == 0 {
		return time.Duration(def) * time.Minute
	}
	return time.Duration(v) * time.Minute
}

func (c CooldownConfig) BillingAuth() time.Duration {
	return minutesOrDefault(c.BillingAuthMinutes, DefaultCooldownBillingAuthMinutes)
}

func (c CooldownConfig) RateLimit() time.Duration {
	return minutesOrDefault(c.RateLimitMinutes, DefaultCooldownRateLimitMinutes)
}

func (c CooldownConfig) BadRequest() time.Duration {
	return minutesOrDefault(c.BadRequestMinutes, DefaultCooldownBadRequestMinutes)
}

func (c CooldownConfig) ClientError() time.Duration {
	return minutesOrDefault(c.ClientErrorMinutes, DefaultCooldownClientErrorMinutes)
}

func (c CooldownConfig) ServerError() time.Duration {
	return minutesOrDefault(c.ServerErrorMinutes, DefaultCooldownServerErrorMinutes)
}

type ChannelsConfig struct {
	Telegram []TelegramBotConfig `json:"telegram"`
	SecMsg   []SecMsgConfig      `json:"secmsg"`
	Discord  DiscordConfig       `json:"discord"`
	Slack    SlackConfig         `json:"slack"`
	Matrix   MatrixConfig        `json:"matrix"`
	LINE     LINEConfig          `json:"line"`
	WebUI    WebUIConfig         `json:"webui"`
	Device   DeviceChannelConfig `json:"device"`
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

// SecMsgConfig defines one secure-messaging daemon speaking the secmsg JSON-RPC
// protocol (e.g. sigd for Signal). A single daemon can host several accounts, so
// each daemon entry lists the accounts to bind; every account becomes its own
// ClawEh channel (own name, allowlist, and agent binding). The daemon is
// service-agnostic: ClawEh learns the concrete service from the handshake.
type SecMsgConfig struct {
	// Name identifies the daemon connection and is the default prefix for each
	// account's channel name. Recommended: the service, e.g. "signal". Empty
	// falls back to "secmsg".
	Name string `json:"name"`
	// Enabled gates whether this daemon's accounts are started.
	Enabled bool `json:"enabled"`
	// Address is the daemon endpoint (host:port) for the secmsg JSON-RPC socket.
	Address string `json:"address"`
	// AllowFrom is the daemon-level allowlist inherited by every account. Accounts
	// discovered from the daemon (when Accounts is empty) bind with this list; an
	// explicit account entry with its own AllowFrom overrides it.
	AllowFrom FlexibleStringSlice `json:"allow_from,omitempty"`
	// GroupTrigger is the daemon-level group-trigger default inherited by every
	// account, overridable per explicit account entry.
	GroupTrigger GroupTriggerConfig `json:"group_trigger,omitempty"`
	// Accounts optionally pins specific accounts to bind with per-account overrides.
	// Empty means ClawEh discovers the daemon's linked accounts and binds one
	// channel per account, each inheriting the daemon-level defaults above.
	Accounts []SecMsgAccountConfig `json:"accounts,omitempty"`
}

// WithDefaults returns a copy of the account with daemon-level defaults filled in
// where the account left them unset. Used both for explicit account entries and
// for accounts synthesized from daemon discovery.
func (s SecMsgConfig) WithDefaults(a SecMsgAccountConfig) SecMsgAccountConfig {
	if len(a.AllowFrom) == 0 {
		a.AllowFrom = s.AllowFrom
	}
	if !a.GroupTrigger.MentionOnly && len(a.GroupTrigger.Prefixes) == 0 {
		a.GroupTrigger = s.GroupTrigger
	}
	return a
}

// SecMsgAccountConfig binds one account on a daemon to a ClawEh channel.
type SecMsgAccountConfig struct {
	// Account is the daemon account id (e.g. "droid1"). Empty auto-selects the
	// daemon's sole linked account (only valid when the daemon has exactly one).
	Account string `json:"account,omitempty"`
	// Name overrides the ClawEh channel name for this account. Empty derives it
	// as "<daemon-name>-<account>".
	Name         string              `json:"name,omitempty"`
	AllowFrom    FlexibleStringSlice `json:"allow_from,omitempty"`
	GroupTrigger GroupTriggerConfig  `json:"group_trigger,omitempty"`
}

// ChannelName returns the routing identifier for this account's channel.
func (a SecMsgAccountConfig) ChannelName(daemon SecMsgConfig) string {
	if a.Name != "" {
		return a.Name
	}
	base := daemon.Name
	if base == "" {
		base = "secmsg"
	}
	if a.Account == "" {
		return base
	}
	return base + "-" + a.Account
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
	Enabled        bool                `json:"enabled"                     env:"CLAW_CHANNELS_WEBUI_ENABLED"`
	Token          string              `json:"token"                       env:"CLAW_CHANNELS_WEBUI_TOKEN"`
	PingInterval   int                 `json:"ping_interval,omitempty"`
	ReadTimeout    int                 `json:"read_timeout,omitempty"`
	WriteTimeout   int                 `json:"write_timeout,omitempty"`
	MaxConnections int                 `json:"max_connections,omitempty"`
	AllowFrom      FlexibleStringSlice `json:"allow_from"                  env:"CLAW_CHANNELS_WEBUI_ALLOW_FROM"`
	Placeholder    PlaceholderConfig   `json:"placeholder,omitempty"`
}

type DevicesConfig struct {
	Enabled    bool `json:"enabled"     env:"CLAW_DEVICES_ENABLED"`
	MonitorUSB bool `json:"monitor_usb" env:"CLAW_DEVICES_MONITOR_USB"`
}

// DeviceChannelConfig configures the external-device gateway: an OpenClaw
// Gateway-protocol WebSocket endpoint that hardware devices (e.g. the Rabbit R1)
// connect to. It runs on its OWN listener (Host/Port) independent of the WebUI/
// admin port, so it can be exposed to the network without exposing the
// unauthenticated WebUI. Distinct from DevicesConfig (USB hardware monitor).
type DeviceChannelConfig struct {
	Enabled bool   `json:"enabled"                env:"CLAW_CHANNELS_DEVICE_ENABLED"`
	Token   string `json:"token"                  env:"CLAW_CHANNELS_DEVICE_TOKEN"` // shared gateway auth token presented in the QR
	// WordToken is a human-typeable passphrase (5 BIP39 words) accepted as an
	// alternative shared token, for clients where the user types the token by hand
	// instead of scanning the QR. Authenticates equivalently to Token.
	WordToken string `json:"word_token,omitempty" env:"CLAW_CHANNELS_DEVICE_WORD_TOKEN"`
	// Host is the device listener bind address: 127.0.0.1 (loopback, default) or
	// 0.0.0.0 to listen for local-network connections. Port defaults to
	// device.DefaultDevicePort (18791) when unset.
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	// AllowedCIDRs restricts which client IPs may reach the device listener. Empty
	// allows any (the gateway is authenticated); loopback is always allowed.
	AllowedCIDRs []string `json:"allowed_cidrs,omitempty"`
	// ExternalURL is the endpoint advertised to devices in the QR. Empty defaults to
	// http://<lan-ip>:<port>. Set to e.g. https://claw.example.com behind a reverse
	// proxy / Cloudflare (https maps to wss).
	ExternalURL string `json:"external_url,omitempty"`
	// AutoApprove skips operator approval for fresh device pairings; default
	// off. Allowed only with Host on loopback (see ValidateExposure).
	AutoApprove bool `json:"auto_approve,omitempty"`
	// TLS serves the device listener over TLS (wss://) on the same port, with
	// the certificate the WebUI HTTPS listener uses. Default off, so existing
	// installs and paired devices keep connecting over ws://. Requires
	// gateway.tls.mode other than "off" (see validateListeners).
	TLS          bool                `json:"tls"`
	AllowOrigins []string            `json:"allow_origins,omitempty"`
	AllowFrom    FlexibleStringSlice `json:"allow_from"             env:"CLAW_CHANNELS_DEVICE_ALLOW_FROM"`
}

type VoiceConfig struct {
	EchoTranscription bool `json:"echo_transcription" env:"CLAW_VOICE_ECHO_TRANSCRIPTION"`
	// STT is an ordered list of speech-to-text backends. The first enabled entry
	// with an API key is used to transcribe inbound audio; the rest are reserved
	// for future fallback. Empty list falls back to legacy provider auto-detect.
	STT []STTProvider `json:"stt,omitempty"`
}

// STTProvider configures one OpenAI-compatible Whisper transcription backend.
// BaseURL and Model default from the provider preset when left blank.
type STTProvider struct {
	Provider string `json:"provider"` // groq | openai | openrouter | custom
	Enabled  bool   `json:"enabled"`
	APIKey   string `json:"api_key,omitempty"`
	BaseURL  string `json:"base_url,omitempty"` // preset default when blank
	Model    string `json:"model,omitempty"`    // preset default when blank
}

type LoggingConfig struct {
	File    bool   `json:"file"                env:"CLAW_LOGGING_FILE"`
	Console bool   `json:"console"             env:"CLAW_LOGGING_CONSOLE"`
	Level   string `json:"level"               env:"CLAW_LOGGING_LEVEL"`
	JSON    bool   `json:"json"                env:"CLAW_LOGGING_JSON"`
	// RetentionDays is how many days of rolled daily logs (YYYYMMDD-claw.log) to
	// keep. The active claw.log is rolled at local midnight (and, if the gateway
	// was down at midnight, as soon as it next starts). 0 keeps logs forever.
	RetentionDays int `json:"retention_days"      env:"CLAW_LOGGING_RETENTION_DAYS"`
	// LogMessageContent controls whether inbound message text and API request/response
	// bodies are included in log entries. Defaults to false to protect user privacy.
	LogMessageContent bool `json:"log_message_content" env:"CLAW_LOGGING_MESSAGE_CONTENT"`
	// DumpRefusals, when true, writes the full LLM input and output to a file
	// in logs/dumps/ whenever the provider returns finish_reason "refusal".
	DumpRefusals bool `json:"dump_refusals" env:"CLAW_LOGGING_DUMP_REFUSALS"`
	// DumpAll, when true, writes the full LLM input and output to a file
	// in logs/dumps/ for every LLM response, regardless of finish reason.
	DumpAll bool `json:"dump_all" env:"CLAW_LOGGING_DUMP_ALL"`
	// DumpFailedCompressions, when true, writes the summarization request and the
	// raw model response to a file in logs/dumps/ whenever a summarization
	// (context compaction) attempt fails — an API error, a non-JSON response, or
	// a rejected summary. Diagnostic only.
	DumpFailedCompressions bool `json:"dump_failed_compressions" env:"CLAW_LOGGING_DUMP_FAILED_COMPRESSIONS"`
}

// Provider is a named endpoint a model is reached through. It owns the wire
// protocol, the base URL, the credentials, and endpoint-scoped quirks. Models
// reference a provider by Name; the WebUI groups models by provider.
type Provider struct {
	Name string `json:"name"` // Unique identifier referenced by ModelConfig.Provider
	// Protocol is the wire format: openai-chat, openai-responses, azure,
	// anthropic, anthropic-messages, claude-cli, codex-cli, antigravity-cli,
	// cursor-cli. "anthropic" and "anthropic-messages" are the same thing —
	// Anthropic speaks one wire format — and both reach the Messages adapter.
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	Proxy    string `json:"proxy,omitempty"`
	// Endpoint-scoped openai-compat knobs.
	StrictCompat bool `json:"strict_compat,omitempty"`
	// RequireReasoningContent backfills a placeholder into reasoning_content on
	// assistant messages that carry none, for endpoints that reject history
	// missing the field. It is the exact opposite of StrictCompat, which strips
	// the field for endpoints that reject its presence — set one or the other,
	// never both.
	//
	// The case this exists for is DeepSeek V4 thinking mode: when a request
	// includes tools, DeepSeek requires reasoning_content on every assistant
	// message in history and answers 400 otherwise, even for turns that made no
	// tool call. Without the backfill, switching an agent from a CLI model (which
	// records no reasoning) to DeepSeek wedges the session — every turn replays
	// the same reasoning-less history and every turn is rejected.
	//
	// Only ever ADDS a placeholder where the field is empty; real reasoning is
	// passed through untouched.
	RequireReasoningContent bool `json:"require_reasoning_content,omitempty"`
	NoParallelToolCalls     bool `json:"no_parallel_tool_calls,omitempty"`
	ResponseFormatJSON      bool `json:"response_format_json,omitempty"`
	// Command overrides the binary path for CLI protocols (claude-cli, etc.).
	Command string `json:"command,omitempty"`
	// BypassRestrictions ("Allow CLI to bypass restrictions" in the WebUI) passes the
	// CLI's skip-permissions / sandbox-bypass flag on every invocation, so it
	// can run commands and edit files anywhere the service user can, without
	// asking. Off by default: the CLI then runs under its own permission
	// settings, and a tool call it cannot approve is refused. One setting per
	// CLI provider, shared by all of its models.
	BypassRestrictions bool `json:"bypass_restrictions,omitempty"`
}

// ModelConfig represents a model-centric provider configuration.
// It allows adding new providers (especially OpenAI-compatible ones) via configuration only.
// The model field uses protocol prefix format: [protocol/]model-identifier
// Supported protocols: openai, anthropic, claude-cli, codex-cli, antigravity-cli
// Default protocol is "openai" if no prefix is specified.
// Vision passthrough modes for ModelConfig.Vision.
const (
	VisionOff          = "off"           // default: tool images are not sent to the model
	VisionUserMessage  = "user_message"  // inject images as a follow-up user turn (Chat Completions)
	VisionToolResponse = "tool_response" // attach images to the tool result (Responses API only)
)

type ModelConfig struct {
	// Required fields
	ModelName string `json:"model_name"` // User-facing alias for the model
	Model     string `json:"model"`      // Raw model id the endpoint expects (no claw protocol prefix)
	Provider  string `json:"provider"`   // Name of the Provider this model is reached through

	// Special providers (CLI-based, OAuth, etc.)
	ConnectMode string `json:"connect_mode,omitempty"` // Connection mode: stdio, grpc
	Workspace   string `json:"workspace,omitempty"`    // Workspace path for CLI-based providers

	// Optional optimizations
	RPM            int    `json:"rpm,omitempty"`              // Requests per minute limit
	MaxTokens      int    `json:"max_tokens,omitempty"`       // Maximum tokens per response; overrides agent defaults
	ContextWindow  int    `json:"context_window,omitempty"`   // Actual model context window size in tokens
	MaxTokensField string `json:"max_tokens_field,omitempty"` // Field name for max tokens (e.g., "max_completion_tokens")
	RequestTimeout int    `json:"request_timeout,omitempty"`
	ThinkingLevel  string `json:"thinking_level,omitempty"` // Extended thinking: off|low|medium|high|xhigh|adaptive
	NoTools        bool   `json:"no_tools,omitempty"`       // When true, tools are not passed to this model
	// Vision controls how images returned by tools (e.g. MCP screenshots) reach
	// this model: "off"/"" (default) drops them; "user_message" injects them as a
	// follow-up user turn (works on Chat Completions, where tool messages are
	// text-only); "tool_response" attaches them to the tool result itself (only
	// valid on the Responses API, whose function_call_output accepts images).
	Vision    string            `json:"vision,omitempty"`
	ExtraArgs []string          `json:"extra_args,omitempty"` // Additional CLI arguments appended after required flags
	Env       map[string]string `json:"env,omitempty"`        // Environment variables for CLI-based providers (merged with os.Environ)
	Enabled   bool              `json:"enabled"`              // If false, model is skipped in all operations

	// ResponseLogFile, when non-empty, causes every raw HTTP response body from
	// the openai_compat provider to be appended to the given path. Diagnostic
	// feature only; no rotation, no expansion of ~ or env vars. Ignored by
	// providers other than openai_compat.
	ResponseLogFile string `json:"response_log_file,omitempty"`

	// ReasoningEffort sets the OpenAI-style reasoning_effort request field for
	// models that natively accept it (notably Grok). Valid values are "none",
	// "low", "medium", "high", or empty. Empty omits the field entirely; "none"
	// is sent explicitly (e.g. to disable reasoning on models that support it).
	// Providers that don't understand the field will silently ignore it.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// ExtraBody is a free-form passthrough map merged into the JSON request
	// body for OpenAI-compatible providers. Use it for per-provider knobs that
	// claw does not model natively. Keys colliding with claw-managed fields
	// (see reservedRequestBodyKeys) are rejected at config load.
	ExtraBody map[string]any `json:"extra_body,omitempty"`

	// DropParams lists top-level request-body fields to strip before sending to
	// OpenAI-compatible providers. Use it to suppress a parameter a model or
	// upstream rejects — e.g. "temperature" for OpenRouter reasoning models that
	// don't advertise it and would 404 under provider.require_parameters.
	// Stripping is applied last (after extra_body), so it always wins. It is a
	// literal filter: listing structural fields like "messages" or "model" will
	// break the request. Ignored by providers other than openai_compat.
	DropParams []string `json:"drop_params,omitempty"`

	// StrictAlternation rewrites the outbound message list for chat-only models
	// that require strict user/assistant alternation and reject system/tool roles
	// (e.g. Gemma on some gateways): the system prompt is folded into the first
	// user turn, tool results become user turns, and consecutive same-role
	// messages are merged. It is model-scoped because models on the same endpoint
	// differ (Gemma needs it; Claude/Nova don't). Pair with no_tools, since
	// tool_calls are dropped. Ignored by providers other than openai_compat.
	StrictAlternation bool `json:"strict_alternation,omitempty"`
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
		return errors.New("model_name is required")
	}
	if c.Model == "" {
		return errors.New("model is required")
	}
	if c.Provider == "" {
		return fmt.Errorf("model %q: provider is required", c.ModelName)
	}
	switch c.ReasoningEffort {
	case "", "none", "low", "medium", "high":
		// ok
	default:
		return fmt.Errorf(
			"model %q: invalid reasoning_effort %q (valid: none, low, medium, high, or omit)",
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
	// Host is the plain-HTTP listener's bind address. Loopback ("",
	// 127.0.0.1, localhost, ::1) — the default — binds 127.0.0.1 and [::1]
	// only. Anything else (a LAN address, a hostname, 0.0.0.0) serves plain
	// HTTP on the network too; that is allowed but unencrypted, and the
	// configuration report marks it. The HTTPS listener does not follow Host:
	// it is placed by TLS.Mode. See HTTPBindHosts and HTTPSBindHosts.
	Host string `json:"host" env:"CLAW_GATEWAY_HOST"`
	// Port is the plain-HTTP listener's port (default 18790).
	Port int `json:"port" env:"CLAW_GATEWAY_PORT"`
	// TLSPort is the HTTPS listener's port (default 18443). It is separate from
	// Port because the two listeners bind different addresses; they share one
	// handler chain.
	TLSPort int `json:"tls_port,omitempty" env:"CLAW_GATEWAY_TLS_PORT"`
	// TLS places the HTTPS listener (Mode) and names the certificate it
	// presents. Empty means HTTPS on all interfaces with a self-signed
	// certificate generated under <CLAW_HOME>/tls.
	TLS TLSConfig `json:"tls,omitempty"`
	// ExternalURL is the base URL advertised to external clients (e.g. the
	// claw-auth OAuth utility) for reaching this gateway's HTTP API, and the
	// name the operator browses to. Empty derives it from the listeners (see
	// EffectiveExternalURL); set it to e.g. https://claw.example.com:18443 to
	// reach the WebUI by a host name, or to a reverse proxy's URL. Its host is
	// added to the self-signed certificate and to the accepted Host names.
	ExternalURL string `json:"external_url,omitempty" env:"CLAW_GATEWAY_EXTERNAL_URL"`
	// AllowedCIDRs is the IP allowlist for the shared HTTP server (WebUI/API +
	// health). Empty means loopback only: the WebUI and /api/* have no operator
	// authentication, so nothing off-box reaches them until an allowlist is set
	// deliberately. Binding to 0.0.0.0 does not by itself widen access — the
	// allowlist is a second, independent gate.
	//
	// To reach it from elsewhere, list the networks explicitly, e.g.
	// ["192.168.1.0/24"] or the RFC1918 set (see PrivateNetworkCIDRs). Use
	// ["*"] (AllowAnyAddress) to allow any address in either family — note that
	// "0.0.0.0/0" is an IPv4 prefix and still refuses IPv6 clients. Loopback is
	// always allowed.
	AllowedCIDRs []string `json:"allowed_cidrs,omitempty"`
	// LockoutExempt lists client addresses (IPs or CIDRs) that are never
	// locked out by a per-address lockout: the WebUI login limiter's address
	// table and the device gateway's auth-failure lockout. Loopback is always
	// exempt. Per-account login locks still apply. See LockoutExemptSet.
	LockoutExempt []string `json:"lockout_exempt,omitempty"`
	// TrustedProxies lists reverse-proxy addresses (IPs or CIDRs). A request
	// whose TCP peer is one of them is attributed to the address in its
	// X-Real-IP header (else the first X-Forwarded-For entry) for the IP
	// allowlists, the per-address lockouts, lockout_exempt, logs and the audit
	// log, on the WebUI/API listeners and the device gateway. Those headers are
	// ignored from anyone else. Loopback is not implied. See TrustedProxySet.
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
}

// TLSConfig is the HTTPS listener's placement and certificate. Mode says
// where HTTPS is served (TLSModeAll, the default; TLSModeLocalhost;
// TLSModeOff). CertFile and KeyFile are PEM paths (anywhere on disk) and go
// together: setting one without the other is a config error. With neither set
// the gateway generates and maintains a self-signed certificate under
// <CLAW_HOME>/tls whose names are localhost, the loopback addresses, the host
// name, its FQDN, every non-loopback interface address, the host of
// external_url and ExtraNames.
type TLSConfig struct {
	// Mode is "all" (HTTPS on every interface; empty means this), "localhost"
	// (127.0.0.1 and [::1] only) or "off" (no HTTPS listener).
	Mode     string `json:"mode,omitempty" env:"CLAW_GATEWAY_TLS_MODE"`
	CertFile string `json:"cert_file,omitempty" env:"CLAW_GATEWAY_TLS_CERT_FILE"`
	KeyFile  string `json:"key_file,omitempty"  env:"CLAW_GATEWAY_TLS_KEY_FILE"`
	// ExtraNames are additional DNS names or IP addresses for the self-signed
	// certificate (a DNS alias, a NAT address). Ignored with a user certificate.
	ExtraNames []string `json:"extra_names,omitempty"`
}

// HTTPS listener placements, the values of gateway.tls.mode.
const (
	TLSModeAll       = "all"
	TLSModeLocalhost = "localhost"
	TLSModeOff       = "off"
)

// EffectiveMode is Mode with the default applied: empty means TLSModeAll.
// An unknown value is returned as is; Validate rejects it.
func (t TLSConfig) EffectiveMode() string {
	m := strings.ToLower(strings.TrimSpace(t.Mode))
	if m == "" {
		return TLSModeAll
	}
	return m
}

// UserSupplied reports whether the operator provides the certificate.
func (t TLSConfig) UserSupplied() bool {
	return t.CertFile != "" && t.KeyFile != ""
}

// Validate rejects an unknown mode and a half-configured certificate.
func (t TLSConfig) Validate() error {
	switch t.EffectiveMode() {
	case TLSModeAll, TLSModeLocalhost, TLSModeOff:
	default:
		return fmt.Errorf("gateway.tls.mode %q: must be %q, %q or %q", t.Mode, TLSModeAll, TLSModeLocalhost, TLSModeOff)
	}
	if (t.CertFile == "") != (t.KeyFile == "") {
		return errors.New("gateway.tls.cert_file and gateway.tls.key_file must be set together (or both left empty for a self-signed certificate)")
	}
	return nil
}

// DefaultGatewayPort is the default port for the merged claw HTTP server
// (gateway + WebUI on a single mux). It matches DefaultConfig's Gateway.Port.
const DefaultGatewayPort = 18790

// DefaultGatewayTLSPort is the default port of the HTTPS listener.
const DefaultGatewayTLSPort = 18443

// IsLoopbackHost reports whether host names the local host only. Empty is
// loopback: it is what an unset gateway.host means.
func IsLoopbackHost(host string) bool {
	switch strings.TrimSpace(host) {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	default:
		return false
	}
}

// HTTPSEnabled reports whether the HTTPS listener is on: it is unless
// gateway.tls.mode is "off".
func (g GatewayConfig) HTTPSEnabled() bool {
	return g.TLS.EffectiveMode() != TLSModeOff
}

// EffectivePort is the loopback HTTP port, defaulting to DefaultGatewayPort.
func (g GatewayConfig) EffectivePort() int {
	if g.Port == 0 {
		return DefaultGatewayPort
	}
	return g.Port
}

// EffectiveTLSPort is the HTTPS port, defaulting to DefaultGatewayTLSPort.
func (g GatewayConfig) EffectiveTLSPort() int {
	if g.TLSPort == 0 {
		return DefaultGatewayTLSPort
	}
	return g.TLSPort
}

// Validate rejects listener settings the gateway would refuse to start on.
func (g GatewayConfig) Validate() error {
	if err := g.TLS.Validate(); err != nil {
		return err
	}
	if _, err := CompileLockoutExempt(g.LockoutExempt); err != nil {
		return err
	}
	if _, err := CompileTrustedProxies(g.TrustedProxies); err != nil {
		return err
	}
	for _, p := range []struct {
		key  string
		port int
	}{{"gateway.port", g.Port}, {"gateway.tls_port", g.TLSPort}} {
		if p.port < 0 || p.port > 65535 {
			return fmt.Errorf("%s %d is out of valid range (1-65535)", p.key, p.port)
		}
	}
	if g.HTTPSEnabled() && g.EffectiveTLSPort() == g.EffectivePort() {
		return fmt.Errorf("gateway.tls_port %d must differ from gateway.port", g.EffectiveTLSPort())
	}
	return nil
}

// ValidateMCPHostListen rejects an MCP host listen address that is not
// loopback. The MCP host speaks plain HTTP and is meant for CLI providers on
// this machine; anything off-box goes through the HTTPS gateway or a proxy.
// Empty means the default (127.0.0.1:5911).
func ValidateMCPHostListen(listen string) error {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("mcp_host.listen %q: must be host:port", listen)
	}
	if !IsLoopbackHost(host) {
		return fmt.Errorf("mcp_host.listen %q: the MCP host is plain HTTP and must listen on a loopback address (127.0.0.1 or ::1)", listen)
	}
	return nil
}

// validateListeners is the load-time check for every setting a listener is
// bound from: the gateway refuses to start on a failure here rather than
// coming up half-configured.
func (c *Config) validateListeners() error {
	if err := c.Gateway.Validate(); err != nil {
		return err
	}
	if err := c.Channels.Device.validateTLS(c.Gateway); err != nil {
		return err
	}
	if err := c.Channels.Device.ValidateExposure(); err != nil {
		return err
	}
	return ValidateMCPHostListen(c.MCPHost.Listen)
}

// ValidateExposure refuses an enabled device listener on a network address
// (anything but loopback) that would pair or admit devices without a secret:
// auto_approve on, or neither token nor word_token set.
func (d DeviceChannelConfig) ValidateExposure() error {
	if !d.Enabled || IsLoopbackHost(d.Host) {
		return nil
	}
	if d.AutoApprove {
		return errors.New("channels.device.auto_approve must be off when the device listener is on a network address")
	}
	if strings.TrimSpace(d.Token) == "" && strings.TrimSpace(d.WordToken) == "" {
		return errors.New("channels.device.token or channels.device.word_token must be set when the device listener is on a network address")
	}
	return nil
}

// validateTLS refuses channels.device.tls on an enabled device listener when
// gateway.tls.mode is "off": the device listener borrows the HTTPS listener's
// certificate, and with HTTPS off there is none to borrow.
func (d DeviceChannelConfig) validateTLS(gw GatewayConfig) error {
	if d.Enabled && d.TLS && !gw.HTTPSEnabled() {
		return errors.New(`channels.device.tls needs the HTTPS certificate: set gateway.tls.mode to "all" or "localhost", or turn channels.device.tls off`)
	}
	return nil
}

// WebSocketScheme is the scheme devices use to reach the device listener
// directly: "wss" with channels.device.tls, "ws" otherwise.
func (d DeviceChannelConfig) WebSocketScheme() string {
	if d.TLS {
		return "wss"
	}
	return "ws"
}

// AllowAnyAddress is the Gateway.AllowedCIDRs entry meaning "any client
// address, IPv4 or IPv6". Mirrors middleware.AllowAnyAddress; declared here so
// the config package does not depend on the HTTP middleware.
const AllowAnyAddress = "*"

// PrivateNetworkCIDRs is the RFC1918 private range set, offered as a
// ready-made allowlist for a LAN install (`claw install --allowed-cidrs`, or
// the WebUI). It is NOT a default: an empty Gateway.AllowedCIDRs means loopback
// only. See EffectiveAllowedCIDRs.
var PrivateNetworkCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
}

// EffectiveAllowedCIDRs returns the IP allowlist to enforce for the shared HTTP
// port. Nil means loopback only, which is the out-of-the-box posture: the
// WebUI/API carry no operator auth, so an install grants no off-box access
// until someone asks for it. A copy is returned so callers cannot mutate the
// configured slice.
func (g GatewayConfig) EffectiveAllowedCIDRs() []string {
	if len(g.AllowedCIDRs) == 0 {
		return nil
	}
	return append([]string(nil), g.AllowedCIDRs...)
}

// ValidateAllowedCIDRs rejects any entry that is not a valid CIDR.
func ValidateAllowedCIDRs(cidrs []string) error {
	for _, c := range cidrs {
		// "*" means any address, in either family — see middleware.AllowAnyAddress.
		// It is not a CIDR, so it has to be accepted before parsing.
		if strings.TrimSpace(c) == AllowAnyAddress {
			continue
		}
		if _, _, err := net.ParseCIDR(c); err != nil {
			return fmt.Errorf("invalid CIDR %q", c)
		}
	}
	return nil
}

// EffectiveExternalURL returns the base URL external clients should use to reach
// the gateway HTTP API. A non-empty ExternalURL is returned verbatim (operators
// may point it at a host name or an https proxy). Otherwise it follows the
// listeners: with HTTPS on all interfaces it is https://<host name>:<tls_port>
// (the self-signed certificate carries the host name; when that is unknown,
// the primary LAN IP); with HTTPS on localhost only it is
// https://127.0.0.1:<tls_port>; with HTTPS off it is the plain-HTTP listener,
// http://<host>:<port>, where a wildcard bind is replaced by the host name as
// above and a loopback one is 127.0.0.1.
func (g GatewayConfig) EffectiveExternalURL() string {
	if g.ExternalURL != "" {
		return g.ExternalURL
	}
	switch g.TLS.EffectiveMode() {
	case TLSModeAll:
		return "https://" + net.JoinHostPort(advertisedHostName(), strconv.Itoa(g.EffectiveTLSPort()))
	case TLSModeLocalhost:
		return "https://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(g.EffectiveTLSPort()))
	}
	host := strings.Trim(strings.TrimSpace(g.Host), "[]")
	switch {
	case IsLoopbackHost(host):
		host = "127.0.0.1"
	case isWildcardHost(host):
		host = advertisedHostName()
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(g.EffectivePort()))
}

// advertisedHostName is the name a wildcard-bound gateway advertises: the
// machine's host name, else its primary LAN IP, else loopback.
func advertisedHostName() string {
	if h, err := os.Hostname(); err == nil {
		if h = strings.TrimSpace(h); h != "" && !IsLoopbackHost(h) {
			return h
		}
	}
	if ip := primaryLANIP(); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

// NetworkAccess reports whether the gateway binds to all interfaces (0.0.0.0),
// i.e. "network access on". Convenience for API/WebUI surfaces.
func (g GatewayConfig) NetworkAccess() bool {
	return g.Host == "0.0.0.0"
}

// primaryLANIP returns the host's first non-loopback private IPv4, or "".
func primaryLANIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil && ip4.IsPrivate() {
			return ip4.String()
		}
	}
	return ""
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

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path) //nolint:gosec // config path chosen by the operator (CLI flag or env)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	// Probe pass over the generic document: warn about keys no field decodes
	// (a typo would otherwise be ignored silently), and resolve "env:"/"file:"
	// secret references so the struct below carries the real values while the
	// reference itself is remembered for the next save.
	doc, err := decodeDocument(data)
	if err != nil {
		return nil, err
	}
	warnUnknownKeys(doc)
	refs, err := resolveSecretRefs(doc)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if len(refs) > 0 {
		if data, err = json.Marshal(doc); err != nil {
			return nil, err
		}
	}

	// Pre-scan the JSON to check how many models / agents.list entries the
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
	if len(tmp.Models) > 0 {
		cfg.Models = nil
	}
	if len(tmp.Agents.List) > 0 {
		cfg.Agents.List = nil
	}
	// Providers need the same treatment, and for a sharper reason: deleting one
	// shifts every later entry down an index, so each would be decoded onto a
	// *different* default and silently inherit the omitempty flags that default
	// happened to set. Deleting "OpenAI" gave OpenRouter Chat Groq's
	// no_parallel_tool_calls and NVIDIA OpenRouter Strict's strict_compat —
	// wire-behaviour changes to providers the user never touched, made
	// permanent by the next save.
	if len(tmp.Providers) > 0 {
		cfg.Providers = nil
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	cfg.secretRefs = refs

	warnLegacyCompressModel(data)
	warnLegacyMaestroBool(cfg)
	warnShellOverride(cfg)
	warnReservedMounts(cfg)

	if err := env.Parse(cfg); err != nil {
		return nil, err
	}

	// Listener settings are checked at load: the gateway must not start on a
	// half-configured certificate or an off-box MCP host, and the config
	// watcher turns this into a "Config file invalid" alert on a live gateway.
	if err := cfg.validateListeners(); err != nil {
		return nil, err
	}
	if err := cfg.Forum.Limits.Validate(); err != nil {
		return nil, err
	}
	// Agent ids are never rewritten: one not in normal form stops the start
	// (and a live reload keeps the running config) until the operator fixes it.
	if errs := cfg.AgentIDErrors(); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	// Migrate legacy channel config fields to new unified structures
	cfg.migrateChannelConfigs()

	// Fold legacy flat compress_* keys into the nested compression block.
	cfg.migrateCompressionConfigs()

	// launcher-config.json was the retired launcher's separate allowlist file.
	// It is no longer read; the allowlist lives in gateway.allowed_cidrs.
	if lc := filepath.Join(filepath.Dir(path), "launcher-config.json"); fileExists(lc) {
		logger.WarnCF("config", "launcher-config.json is no longer read; move its allowed_cidrs into gateway.allowed_cidrs in config.json and delete it",
			map[string]any{"path": lc})
	}

	// Note: provider/model validation is intentionally NOT fatal here. LoadConfig
	// returns the full parsed config so the WebUI can display and repair invalid
	// entries (a bad provider must not make the config unreadable). The gateway
	// calls PruneInvalid() at startup to drop invalid entries with a WARN and run
	// on the survivors; the WebUI save path validates strictly before persisting.
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
		logger.WarnCF("config", "ignoring removed field agents.defaults.compress_model; configure summarization models globally via summarization.models", nil)
	}
	for _, a := range legacy.Agents.List {
		if len(a.CompressModel) > 0 {
			logger.WarnCF("config", "ignoring removed field compress_model on agent; configure summarization models globally via summarization.models", map[string]any{"agent_id": a.ID})
		}
	}
}

// migrateCompressionConfigs folds the retired flat compress_* keys into the
// nested compression block and clears them, so a config written before the
// split keeps working instead of silently reverting to built-in defaults on the
// next deploy. Each migrated key is logged once at WARN with its new location;
// rewrite config.json and the warnings stop. An explicitly-set nested value
// always wins — migration only fills gaps.
//
// This is a transitional shim. Once deployed configs are rewritten, delete it
// along with the legacy fields it reads.
func (c *Config) migrateCompressionConfigs() {
	migrated := map[string]string{}

	fold := func(scope string, dst **CompressionConfig, legacy legacyCompressKeys) {
		if legacy.empty() {
			return
		}
		if *dst == nil {
			*dst = &CompressionConfig{}
		}
		cfg := *dst
		if cfg.Trigger == nil {
			cfg.Trigger = &CompressionTriggerConfig{}
		}
		if cfg.Retain == nil {
			cfg.Retain = &CompressionRetainConfig{}
		}
		if cfg.Estimate == nil {
			cfg.Estimate = &CompressionEstimateConfig{}
		}
		note := func(from, to string) { migrated[scope+"."+from] = to }
		if legacy.Min != nil && cfg.Trigger.MinPercent == nil {
			cfg.Trigger.MinPercent = legacy.Min
			note("compress_min_percent", "compression.trigger.min_percent")
		}
		if legacy.Normal != nil && cfg.Trigger.NormalPercent == nil {
			cfg.Trigger.NormalPercent = legacy.Normal
			note("compress_normal_percent", "compression.trigger.normal_percent")
		}
		if legacy.Safety != nil && cfg.Trigger.SafetyPercent == nil {
			cfg.Trigger.SafetyPercent = legacy.Safety
			note("compress_safety_percent", "compression.trigger.safety_percent")
		}
		if legacy.MessageThreshold != nil && cfg.Trigger.MessageCount == nil {
			cfg.Trigger.MessageCount = legacy.MessageThreshold
			note("compress_message_threshold", "compression.trigger.message_count")
		}
		if legacy.RetainPercent != nil && cfg.Retain.TokenPercent == nil {
			cfg.Retain.TokenPercent = legacy.RetainPercent
			note("compress_retain_token_percent", "compression.retain.token_percent")
		}
		if legacy.RetainMinMessages != nil && cfg.Retain.MinMessages == nil {
			cfg.Retain.MinMessages = legacy.RetainMinMessages
			note("compress_retain_min_messages", "compression.retain.min_messages")
		}
		if legacy.CharsPerToken != nil && cfg.Estimate.CharsPerToken == nil {
			cfg.Estimate.CharsPerToken = legacy.CharsPerToken
			note("compress_chars_per_token", "compression.estimate.chars_per_token")
		}
		if legacy.SafetyMargin != nil && cfg.Estimate.TokenSafetyMargin == nil {
			cfg.Estimate.TokenSafetyMargin = legacy.SafetyMargin
			note("compress_token_safety_margin", "compression.estimate.token_safety_margin")
		}
	}

	d := &c.Agents.Defaults
	fold("agents.defaults", &d.Compression, legacyCompressKeys{
		Min: d.CompressMinPercent, Normal: d.CompressNormalPercent,
		Safety: d.CompressSafetyPercent, MessageThreshold: d.CompressMessageThreshold,
		RetainPercent: d.CompressRetainTokenPercent, RetainMinMessages: d.CompressRetainMinMessages,
		CharsPerToken: d.CompressCharsPerToken, SafetyMargin: d.CompressTokenSafetyMargin,
	})
	d.CompressMinPercent, d.CompressNormalPercent = nil, nil
	d.CompressSafetyPercent, d.CompressMessageThreshold = nil, nil
	d.CompressRetainTokenPercent, d.CompressRetainMinMessages = nil, nil
	d.CompressCharsPerToken, d.CompressTokenSafetyMargin = nil, nil

	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		fold("agents."+a.ID, &a.Compression, legacyCompressKeys{
			Min: a.CompressMinPercent, Normal: a.CompressNormalPercent,
			Safety: a.CompressSafetyPercent, MessageThreshold: a.CompressMessageThreshold,
			RetainPercent: a.CompressRetainTokenPercent, RetainMinMessages: a.CompressRetainMinMessages,
			CharsPerToken: a.CompressCharsPerToken, SafetyMargin: a.CompressTokenSafetyMargin,
		})
		a.CompressMinPercent, a.CompressNormalPercent = nil, nil
		a.CompressSafetyPercent, a.CompressMessageThreshold = nil, nil
		a.CompressRetainTokenPercent, a.CompressRetainMinMessages = nil, nil
		a.CompressCharsPerToken, a.CompressTokenSafetyMargin = nil, nil
	}

	if len(migrated) > 0 {
		logger.WarnCF("config", "migrated legacy compress_* keys into the nested compression block; update config.json to silence this",
			map[string]any{"migrated": migrated})
	}
}

// legacyCompressKeys is the flat compress_* set as it appeared on both
// AgentConfig and AgentDefaults, gathered so one fold routine serves each.
type legacyCompressKeys struct {
	Min               *int
	Normal            *int
	Safety            *int
	MessageThreshold  *int
	RetainPercent     *int
	RetainMinMessages *int
	CharsPerToken     *float64
	SafetyMargin      *float64
}

func (l legacyCompressKeys) empty() bool {
	return l.Min == nil && l.Normal == nil && l.Safety == nil && l.MessageThreshold == nil &&
		l.RetainPercent == nil && l.RetainMinMessages == nil &&
		l.CharsPerToken == nil && l.SafetyMargin == nil
}

func (c *Config) migrateChannelConfigs() {
	// Discord: mention_only -> group_trigger.mention_only
	if c.Channels.Discord.MentionOnly && !c.Channels.Discord.GroupTrigger.MentionOnly {
		c.Channels.Discord.GroupTrigger.MentionOnly = true
	}
}

// SaveConfig persists cfg. Any save through this path marks the config as
// user-touched (default_config=false), so the setup wizard can tell a fresh,
// never-saved install from a configured one.
func SaveConfig(path string, cfg *Config) error {
	cfg.DefaultConfig = false
	return writeConfig(path, cfg)
}

// SeedDefaultConfig writes the initial auto-generated config to disk, preserving
// the default_config marker (unlike SaveConfig, which clears it). Use only for
// the first-run seed.
func SeedDefaultConfig(path string, cfg *Config) error {
	return writeConfig(path, cfg)
}

func writeConfig(path string, cfg *Config) error {
	// Secret references go back in place of the values they resolved to, so a
	// save never copies an env var or key file into config.json.
	compact, err := MarshalWithSecretRefs(cfg)
	if err != nil {
		return err
	}
	var data bytes.Buffer
	if err := json.Indent(&data, compact, "", "  "); err != nil {
		return err
	}

	// Use unified atomic write utility with explicit sync for flash storage reliability.
	return fileutil.WriteFileAtomic(path, data.Bytes(), 0o600)
}

// HasCLIProvider reports whether any enabled model in ModelList uses a
// *-cli protocol (claude-cli, codex-cli, gemini-cli). Those CLIs rely on
// the MCP host to call claw's tools natively.
func (c *Config) HasCLIProvider() bool {
	for i := range c.Models {
		if !c.Models[i].Enabled {
			continue
		}
		prov, err := c.GetProvider(c.Models[i].Provider)
		if err != nil {
			continue
		}
		if IsCLIProtocol(prov.Protocol) {
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

// BaseDir returns the base directory under which agent workspaces live. An
// explicit agents.base_dir wins; otherwise it defaults to <data_dir>/agents.
func (c *Config) BaseDir() string {
	if c.Agents.BaseDir != "" {
		return expandHome(c.Agents.BaseDir)
	}
	return filepath.Join(c.dataDir, "agents")
}

// ResolveCommonDir returns the global shared directory agents read/write via the
// "common" tools. An explicit agents.common_dir wins; otherwise it defaults to
// <data_dir>/common.
func (c *Config) ResolveCommonDir() string {
	if c.Agents.CommonDir != "" {
		return expandHome(c.Agents.CommonDir)
	}
	return filepath.Join(c.dataDir, global.CommonDir)
}

// InternalPath returns the directory for claw's own state that nobody edits
// by hand (<data_dir>/internal): token stores, the device database.
func (c *Config) InternalPath() string {
	return filepath.Join(c.dataDir, global.InternalDir)
}

// CLIPath returns the working directory CLI providers run in when their model
// sets no workspace (<data_dir>/cli).
func (c *Config) CLIPath() string {
	return filepath.Join(c.dataDir, global.CLIDir)
}

// AgentSessionDirs returns the sessions subdirectory for every configured
// agent, deduped. This mirrors the workspace resolution logic in
// agentreg.ConfigWorkspace. The result is used by the
// WebUI to enumerate sessions across all configured agents.
func (c *Config) AgentSessionDirs() []string {
	base := c.BaseDir()

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
			id = "default"
		}
		add(filepath.Join(base, id))
	}

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

// FusionPath returns the MCPFusion config directory (~/.claw/fusion), holding the
// JSON service definitions, an optional env file, and fusion.log.
func (c *Config) FusionPath() string {
	return filepath.Join(c.dataDir, "fusion")
}

// FusionTokensPath returns the shared SQLite store for fusion OAuth tokens and
// auth codes (~/.claw/internal/fusion-tokens.db).
func (c *Config) FusionTokensPath() string {
	return filepath.Join(c.InternalPath(), "fusion-tokens.db")
}

// BackupConfig controls the nightly configuration backup of key files
// (config.json and the cron jobs file) into <data dir>/backup/YYYYMMDD/.
type BackupConfig struct {
	// Enabled is on by default: nil (field absent) means enabled. Set an explicit
	// false to turn the nightly backup off.
	Enabled    *bool  `json:"enabled,omitempty"`
	At         string `json:"at,omitempty"`          // "HH:MM" local time; default 03:00
	RetainDays int    `json:"retain_days,omitempty"` // prune older archives; default 30
	// Dest is the directory archives are written to (an off-host mount, for
	// example). Empty means <data dir>/backup.
	Dest string `json:"dest,omitempty"`
}

// IsEnabled reports whether the nightly configuration backup runs. It defaults
// to true when unset, so an existing config without a backup block gets backups
// without any edit; set "enabled": false to opt out.
func (b BackupConfig) IsEnabled() bool {
	return b.Enabled == nil || *b.Enabled
}

// BackupAt returns the configured run time as hour and minute, defaulting to
// 03:00 when unset or unparseable.
func (b BackupConfig) BackupAt() (hour, minute int) {
	hour, minute = 3, 0
	parts := strings.SplitN(strings.TrimSpace(b.At), ":", 2)
	if len(parts) == 2 {
		if h, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && h >= 0 && h <= 23 {
			hour = h
		}
		if m, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && m >= 0 && m <= 59 {
			minute = m
		}
	}
	return hour, minute
}

// BackupRetainDays returns the retention window, defaulting to 30 when unset.
func (b BackupConfig) BackupRetainDays() int {
	if b.RetainDays <= 0 {
		return 30
	}
	return b.RetainDays
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			logger.WarnCF("config", "cannot expand ~: home directory unknown", map[string]any{"path": path, "error": err.Error()})
			return path
		}
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
		return nil, fmt.Errorf("model %q not found in models or providers", modelName)
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
	for i := range c.Models {
		if c.Models[i].ModelName == modelName && c.Models[i].Enabled {
			matches = append(matches, c.Models[i])
		}
	}
	return matches
}

// validProtocols is the set of wire protocols a Provider may declare. Each maps
// to an internal provider implementation in providers.
var validProtocols = map[string]struct{}{
	"openai-chat":        {},
	"openai-responses":   {},
	"azure":              {},
	"anthropic":          {},
	"anthropic-messages": {},
	"claude-cli":         {},
	"codex-cli":          {},
	"antigravity-cli":    {},
	// Retained alias: Google deprecated the Gemini CLI in favour of
	// Antigravity, and a released config naming this must keep validating.
	"gemini-cli": {},
	"cursor-cli": {},
	// A model on it represents a person: see human.go.
	HumanProtocol: {},
}

// httpProtocols are the protocols that require a base_url.
var httpProtocols = map[string]struct{}{
	"openai-chat":        {},
	"openai-responses":   {},
	"azure":              {},
	"anthropic":          {},
	"anthropic-messages": {},
}

// IsCLIProtocol reports whether the protocol is a subprocess CLI provider,
// which authenticates out-of-band and needs no API key.
func IsCLIProtocol(protocol string) bool {
	switch protocol {
	// "gemini-cli" is retained as an alias for "antigravity-cli": Google
	// deprecated the Gemini CLI, and a config still naming it must keep
	// starting the MCP host, or its CLI would silently lose every claw tool.
	case "claude-cli", "codex-cli", "antigravity-cli", "gemini-cli", "cursor-cli":
		return true
	default:
		return false
	}
}

// HasCredentials reports whether this provider carries enough to authenticate:
// CLI providers always qualify (they auth out-of-band), and so does the human
// protocol, which reaches a person and needs none; HTTP providers need an API
// key.
func (p *Provider) HasCredentials() bool {
	if IsCLIProtocol(p.Protocol) || IsHumanProtocol(p.Protocol) {
		return true
	}
	return p.APIKey != ""
}

// GetProvider resolves a provider by name. The lookup is case-sensitive on the
// configured Name.
func (c *Config) GetProvider(name string) (*Provider, error) {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i], nil
		}
	}
	return nil, fmt.Errorf("provider %q not found", name)
}

// FindProviderByProtocol returns the first provider declaring the given
// protocol, or nil. Used by flows that target a wire family rather than a
// specific named endpoint (e.g. OAuth login attaching to the anthropic
// provider).
func (c *Config) FindProviderByProtocol(protocol string) *Provider {
	for i := range c.Providers {
		if c.Providers[i].Protocol == protocol {
			return &c.Providers[i]
		}
	}
	return nil
}

// ValidateProviders checks that provider names are unique and non-empty, each
// protocol is recognised, and HTTP protocols carry a base_url.
func (c *Config) ValidateProviders() error {
	seen := make(map[string]struct{}, len(c.Providers))
	for i := range c.Providers {
		p := &c.Providers[i]
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("providers[%d]: name is required", i)
		}
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("providers[%d]: duplicate provider name %q", i, p.Name)
		}
		seen[p.Name] = struct{}{}
		if _, ok := validProtocols[p.Protocol]; !ok {
			return fmt.Errorf("provider %q: unknown protocol %q", p.Name, p.Protocol)
		}
		if _, http := httpProtocols[p.Protocol]; http && p.BaseURL == "" {
			return fmt.Errorf("provider %q: base_url is required for protocol %q", p.Name, p.Protocol)
		}
	}
	return nil
}

// ValidateModels validates all ModelConfig entries in the models,
// including that each model's provider reference resolves to a configured
// provider. Multiple entries with the same model_name are allowed for load
// balancing.
func (c *Config) ValidateModels() error {
	for i := range c.Models {
		if err := c.Models[i].Validate(); err != nil {
			return fmt.Errorf("models[%d]: %w", i, err)
		}
		if _, err := c.GetProvider(c.Models[i].Provider); err != nil {
			return fmt.Errorf("models[%d] (%q): %w", i, c.Models[i].ModelName, err)
		}
	}
	return nil
}

// RenameModelReferences repoints every reference to a model alias from oldName
// to newName across agent defaults, per-agent model chains, image models, and
// the global summarization chain. Used when a model's
// model_name changes via the WebUI so existing references are not orphaned.
// No-op when oldName is empty or unchanged. Mutates in-memory config only.
func (c *Config) RenameModelReferences(oldName, newName string) {
	if oldName == "" || oldName == newName {
		return
	}
	for _, site := range c.modelRefSites() {
		if site.slice != nil {
			renameInSlice(*site.slice, oldName, newName)
		} else {
			*site.scalar = renameScalar(*site.scalar, oldName, newName)
		}
	}
}

func renameScalar(s, oldName, newName string) string {
	if s == oldName {
		return newName
	}
	return s
}

func renameInSlice(ss []string, oldName, newName string) {
	for i := range ss {
		if ss[i] == oldName {
			ss[i] = newName
		}
	}
}

// ValidateProvider validates a single provider (the one at idx) without
// rejecting the whole config because OTHER providers are invalid. It checks the
// provider's own name, protocol, and base_url, plus that its name does not
// collide with another provider. The per-provider WebUI endpoints use this so an
// operator can repair one entry at a time (e.g. during a protocol migration)
// even while other entries remain invalid.
func (c *Config) ValidateProvider(idx int) error {
	if idx < 0 || idx >= len(c.Providers) {
		return fmt.Errorf("provider index %d out of range", idx)
	}
	p := &c.Providers[idx]
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("provider name is required")
	}
	for i := range c.Providers {
		if i != idx && c.Providers[i].Name == p.Name {
			return fmt.Errorf("duplicate provider name %q", p.Name)
		}
	}
	if _, ok := validProtocols[p.Protocol]; !ok {
		return fmt.Errorf("provider %q: unknown protocol %q", p.Name, p.Protocol)
	}
	if _, http := httpProtocols[p.Protocol]; http && p.BaseURL == "" {
		return fmt.Errorf("provider %q: base_url is required for protocol %q", p.Name, p.Protocol)
	}
	return nil
}

// PruneInvalid removes providers and models that fail validation, logging a WARN
// for each dropped entry, and returns how many of each were removed. It is the
// lenient counterpart to ValidateProviders/ValidateModels: the gateway calls it
// at startup so a single bad entry (e.g. a stale/unknown protocol, or a model
// pointing at a missing provider) degrades gracefully instead of aborting the
// whole process. It mutates the in-memory config only — it never writes to disk.
func (c *Config) PruneInvalid() (droppedProviders, droppedModels int) {
	seen := make(map[string]struct{}, len(c.Providers))
	valid := make(map[string]struct{}, len(c.Providers))
	keptProviders := make([]Provider, 0, len(c.Providers))
	for i := range c.Providers {
		p := c.Providers[i]
		var reason string
		if strings.TrimSpace(p.Name) == "" {
			reason = "name is required"
		} else if _, dup := seen[p.Name]; dup {
			reason = "duplicate provider name"
		} else if _, ok := validProtocols[p.Protocol]; !ok {
			reason = fmt.Sprintf("unknown protocol %q", p.Protocol)
		} else if _, http := httpProtocols[p.Protocol]; http && p.BaseURL == "" {
			reason = fmt.Sprintf("base_url is required for protocol %q", p.Protocol)
		}
		if reason != "" {
			logger.WarnCF("config", "ignoring invalid provider", map[string]any{
				"provider": p.Name,
				"reason":   reason,
			})
			droppedProviders++
			continue
		}
		seen[p.Name] = struct{}{}
		valid[p.Name] = struct{}{}
		keptProviders = append(keptProviders, p)
	}
	c.Providers = keptProviders

	keptModels := make([]ModelConfig, 0, len(c.Models))
	for i := range c.Models {
		m := c.Models[i]
		if err := m.Validate(); err != nil {
			logger.WarnCF("config", "ignoring invalid model", map[string]any{
				"model":  m.ModelName,
				"reason": err.Error(),
			})
			droppedModels++
			continue
		}
		if _, ok := valid[m.Provider]; !ok {
			logger.WarnCF("config", "ignoring model with unknown provider", map[string]any{
				"model":    m.ModelName,
				"provider": m.Provider,
			})
			droppedModels++
			continue
		}
		keptModels = append(keptModels, m)
	}
	c.Models = keptModels
	return droppedProviders, droppedModels
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

// fileExists reports whether path names an existing file.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
