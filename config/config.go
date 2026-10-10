package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
