package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/caarlos0/env/v11"

	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/logger"
)

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

// fileExists reports whether path names an existing file.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
