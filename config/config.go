package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
)

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

// fileExists reports whether path names an existing file.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
