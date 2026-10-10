package config

import (
	"errors"
	"strings"
	"time"
)

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
