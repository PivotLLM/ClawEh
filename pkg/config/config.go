package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/PivotLLM/ClawEh/pkg/fileutil"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/logger"
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
	Providers     []Provider          `json:"providers,omitempty"`
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
	// ConfigReloadIntervalSeconds controls how often the daemon polls the config
	// file for changes and triggers a reload. Defaults to
	// global.DefaultConfigReloadIntervalSeconds; floored at
	// global.MinConfigReloadIntervalSeconds.
	ConfigReloadIntervalSeconds int `json:"config_reload_interval_seconds,omitempty" env:"CLAW_CONFIG_RELOAD_INTERVAL_SECONDS"`

	dataDir string // runtime-only: base data directory, not serialized
}

// MarshalJSON implements custom JSON marshaling for Config to omit the session
// section when empty. The providers list omits naturally via its slice tag.
func (c Config) MarshalJSON() ([]byte, error) {
	type Alias Config
	aux := &struct {
		Session *SessionConfig `json:"session,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(&c),
	}

	// Only include session if not empty
	if c.Session.Mode != "" || len(c.Session.IdentityLinks) > 0 {
		aux.Session = &c.Session
	}

	return json.Marshal(aux)
}

type AgentsConfig struct {
	// BaseDir is the base directory under which every agent's workspace lives:
	// each agent resolves to <base_dir>/<agent-id> (the routing-default agent
	// uses <base_dir>/default). A per-agent `workspace` overrides this. Empty
	// defaults to <data_dir>/agents. Point it at another volume to relocate all
	// agent files at once.
	BaseDir string `json:"base_dir,omitempty" env:"CLAW_AGENTS_BASE_DIR"`
	// CommonDir is the global path to the shared directory that agents can read
	// from and write to via the "common" tools. Empty defaults to
	// <agents base>/common (see Config.ResolveCommonDir).
	CommonDir string        `json:"common_dir,omitempty" env:"CLAW_AGENTS_COMMON_DIR"`
	Defaults  AgentDefaults `json:"defaults"`
	List      []AgentConfig `json:"list,omitempty"`
}

// SummarizationConfig is the global, deployment-wide summarization model chain.
// Models are tried in order for context compaction across all agents; each entry
// is a models alias (or a raw protocol/model string). The agent's own
// primary model is always appended as a final last-resort fallback at runtime.
// An empty Models list means summarization runs against each agent's own model.
type SummarizationConfig struct {
	Models []string `json:"models,omitempty"`
	// DebugCapture, when true, appends the verbatim request and response of every
	// summarization LLM invocation to <agent-workspace>/compact.jsonl. Debugging
	// only; off by default.
	DebugCapture bool `json:"debug_capture,omitempty" env:"CLAW_SUMMARIZATION_DEBUG_CAPTURE"`
}

// MemoryConfig configures the cognitive-memory subsystem for an agent. The
// subsystem is ACTIVE for an agent only when that agent is allowed the cogmem
// tools (there is no separate engine flag). The consolidation model is NOT
// configured here: it reuses the agent's summarization ("Memory") model chain
// (SummarizationModels → global Summarization.Models → the agent's own model).
type MemoryConfig struct {
	Prompt        MemoryPromptConfig        `json:"prompt"`
	Consolidation MemoryConsolidationConfig `json:"consolidation"`
	Retention     MemoryRetentionConfig     `json:"retention"`
	Export        MemoryExportConfig        `json:"export"`
}

// MemoryPromptConfig tunes per-turn prompt composition.
type MemoryPromptConfig struct {
	TopKDomains       int     `json:"top_k_domains"`
	MaxChars          int     `json:"max_chars"`
	MinConfidence     float64 `json:"min_confidence"`
	IncludeDebugTrace bool    `json:"include_debug_trace"`
	PendingSurface    string  `json:"pending_surface"` // "ask" | "export_only"
	PendingMax        int     `json:"pending_max"`
}

// MemoryConsolidationConfig tunes the background sleep cycle.
type MemoryConsolidationConfig struct {
	EveryNMessages   int    `json:"every_n_messages"`
	IdleMinutes      int    `json:"idle_minutes"`
	Nightly          bool   `json:"nightly"`
	NightlyAt        string `json:"nightly_at"`
	ProposeDomains   bool   `json:"propose_domains"`
	AutoPromote      bool   `json:"auto_promote"`
	DebugDump        bool   `json:"debug_dump"`
	MaxBatchMessages int    `json:"max_batch_messages"`
	MaxInputTokens   int    `json:"max_input_tokens"`
	PerMessageChars  int    `json:"per_message_chars"`
	MaxOutputTokens  int    `json:"max_output_tokens"`
	MaxRuntimeSecs   int    `json:"max_runtime_seconds"`
}

// MemoryRetentionConfig guards unconsolidated archive messages from pruning.
type MemoryRetentionConfig struct {
	ProtectUnconsolidated bool `json:"protect_unconsolidated"`
}

// MemoryExportConfig controls the read-only GENERATED_*.md export.
type MemoryExportConfig struct {
	Enabled bool `json:"enabled"`
}

type AgentConfig struct {
	ID          string           `json:"id"`
	Enabled     *bool            `json:"enabled,omitempty"`
	Default     bool             `json:"default,omitempty"`
	Name        string           `json:"name,omitempty"`
	Workspace   string           `json:"workspace,omitempty"`
	Models      []string         `json:"models,omitempty"`
	Skills      []string         `json:"skills,omitempty"`
	Tools       []string         `json:"tools,omitempty"`
	Subagents   *SubagentsConfig `json:"subagents,omitempty"`
	Message     *MessageConfig   `json:"message,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`

	// GlobalCron lets this agent create and manage cron jobs for OTHER agents
	// (by passing their agent id). Off by default: an agent can only schedule for
	// itself. Typically exactly one orchestrator agent has this.
	GlobalCron bool `json:"global_cron,omitempty"`

	// Maestro is an all-or-nothing toggle for the Maestro task-orchestration tool
	// suite (projects, playbooks, tasks). Off by default. When on, the agent gets
	// the entire Maestro toolset, with per-agent data under <workspace>/maestro.
	Maestro bool `json:"maestro,omitempty"`

	// Cogmem is an all-or-nothing toggle for the cognitive-memory tool suite and
	// subsystem (prompt injection, archive hook, consolidation). It is an optional
	// bool so the default is ON: nil (key absent) or true ⇒ enabled; false ⇒
	// disabled. Gated as a unit, not via the per-tool allowlist.
	Cogmem *bool `json:"cogmem,omitempty"`

	// ShareCommon toggles the per-agent "common" shared-directory tools. nil or
	// true (the default) exposes them; false withholds them from this agent.
	ShareCommon *bool `json:"share_common,omitempty"`

	// Memory optionally overrides the agent-defaults memory config wholesale
	// (nil → use AgentDefaults.Memory). Only meaningful when the agent is
	// allowed the cogmem tools.
	Memory *MemoryConfig `json:"memory,omitempty"`

	// SummarizationModels is an optional per-agent summarization model chain.
	// When non-empty, these models are tried first (in order) for this agent's
	// context compaction, ahead of the global summarization.models list and the
	// agent's own model. Use it to give an agent uncensored/specialised
	// summarizers when the default models refuse its content (e.g. security or
	// fiction topics). Resolution order: agent-specific → global → agent's model.
	SummarizationModels []string `json:"summarization_models,omitempty"`

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

	// ContextEviction overrides the per-turn tool-result eviction policy for
	// this agent. Unset fields fall back to the defaults block, then to the
	// built-in defaults.
	ContextEviction *ContextEvictionConfig `json:"context_eviction,omitempty"`

	// Mounts expose external directory trees as top-level names in this agent's
	// space (peers of files/ and skills/), accessed as <name>/... Per agent.
	Mounts []MountConfig `json:"mounts,omitempty"`
}

// MountConfig mounts an external directory tree as a top-level name in an agent's
// space, beside files/ and skills/. The whole tree under Path is reachable as
// `<Name>/...`; access is sandboxed to the mount (no `..` escape). Read + write.
type MountConfig struct {
	Name string `json:"name"` // single path component, [A-Za-z0-9-] only
	Path string `json:"path"` // absolute external directory
	// Notify watches the mount tree for new files and notifies the agent on its
	// default channel (cron-style) when one appears.
	Notify bool `json:"notify,omitempty"`
}

var mountNameRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// reservedMountNames cannot be used as mount names — they would shadow the
// built-in workspace roots.
var reservedMountNames = map[string]bool{"files": true, "skills": true, "tasks": true, "common": true}

// ValidateMountName checks a mount name: a single path component of letters,
// digits, and hyphens, not colliding with a reserved root.
func ValidateMountName(name string) error {
	if !mountNameRe.MatchString(name) {
		return fmt.Errorf("mount name %q: use only letters, digits, and '-' (a single directory name)", name)
	}
	if reservedMountNames[strings.ToLower(name)] {
		return fmt.Errorf("mount name %q is reserved", name)
	}
	return nil
}

// ContextEvictionConfig controls the per-turn, LLM-free eviction sweep that
// collapses re-retrievable tool results (file reads, web fetches) in the live
// window to a placeholder so long sessions rarely trigger summarization
// compaction. All fields are pointers so a per-agent block overrides the
// defaults block field by field; an unset field falls back to the built-in
// default (see llmcontext.DefaultEvictionPolicy).
type ContextEvictionConfig struct {
	Enabled      *bool `json:"enabled,omitempty"`       // nil => enabled
	ProtectTurns *int  `json:"protect_turns,omitempty"` // nil => 3
	EvictTurns   *int  `json:"evict_turns,omitempty"`   // nil => 10
	BudgetBytes  *int  `json:"budget_bytes,omitempty"`  // nil => derived (~40% of window)
	NotifyUser   *bool `json:"notify_user,omitempty"`   // nil => off
}

// IsEnabled returns true if the agent is enabled (nil means enabled by default).
func (a *AgentConfig) IsEnabled() bool {
	return a.Enabled == nil || *a.Enabled
}

// SharesCommon reports whether this agent gets the "common" shared-directory
// tools. The default is ON: a nil agent or an unset ShareCommon shares.
func (a *AgentConfig) SharesCommon() bool {
	return a == nil || a.ShareCommon == nil || *a.ShareCommon
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

// CognitiveMemoryEnabled reports whether the cognitive-memory suite + subsystem
// (tools, prompt injection, archive hook, consolidation) is active for this
// agent. It is the per-agent `cogmem` toggle, defaulting ON: nil or true ⇒
// enabled; false ⇒ disabled. (Previously keyed off the per-tool allowlist; it is
// now an all-or-nothing suite gated as a unit.)
func (a *AgentConfig) CognitiveMemoryEnabled() bool {
	if a == nil {
		return false
	}
	return a.Cogmem == nil || *a.Cogmem
}

type SubagentsConfig struct {
	AllowAgents []string `json:"allow_agents,omitempty"`
	Models      []string `json:"models,omitempty"`
}

// MessageConfig controls the rotating-token external-message system for an agent.
// WindowMinutes==0 (or omitted) disables the endpoint entirely.
type MessageConfig struct {
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
	// Default marks this binding as the agent's default delivery channel — where
	// cron jobs (and other agent-targeted output) are sent. At most one default
	// per agent, and a default must resolve to a concrete chat: either a concrete
	// Match.Peer{Kind,ID}, or an explicit DeliverTo. See Config.CronTarget /
	// ValidateBindings.
	Default bool `json:"default,omitempty"`
	// DeliverTo is an explicit chat/peer id used ONLY for async (cron) delivery on
	// this binding's channel — never for routing. It exists for channels whose
	// Match has no concrete peer (e.g. a Telegram bot bound broadly to an agent):
	// set it to the chat id cron output should go to. DeliverPeerKind defaults to
	// "direct".
	DeliverTo       string `json:"deliver_to,omitempty"`
	DeliverPeerKind string `json:"deliver_peer_kind,omitempty"`
}

type SessionConfig struct {
	Mode          string              `json:"mode,omitempty"`
	IdentityLinks map[string][]string `json:"identity_links,omitempty"`
}

// DefaultBinding returns the agent's binding marked Default, or false if none.
// Agent ids are matched case-insensitively: binding agent_ids are author-cased
// (e.g. "Karen") while a session-derived caller id is lowercased ("karen").
func (c *Config) DefaultBinding(agentID string) (*AgentBinding, bool) {
	id := strings.TrimSpace(agentID)
	for i := range c.Bindings {
		b := &c.Bindings[i]
		if b.Default && strings.EqualFold(b.AgentID, id) {
			return b, true
		}
	}
	return nil, false
}

// AgentHasGlobalCron reports whether the agent may schedule/manage cron jobs for
// other agents.
func (c *Config) AgentHasGlobalCron(agentID string) bool {
	id := strings.TrimSpace(agentID)
	for i := range c.Agents.List {
		if strings.EqualFold(c.Agents.List[i].ID, id) {
			return c.Agents.List[i].GlobalCron
		}
	}
	return false
}

// AgentHasMaestro reports whether the agent has the Maestro tool suite enabled.
func (c *Config) AgentHasMaestro(agentID string) bool {
	id := strings.TrimSpace(agentID)
	for i := range c.Agents.List {
		if strings.EqualFold(c.Agents.List[i].ID, id) {
			return c.Agents.List[i].Maestro
		}
	}
	return false
}

// AgentSuiteEnabled reports whether the named all-or-nothing tool suite is
// enabled for the agent. Suites are gated as a unit by a per-agent flag rather
// than the per-tool allowlist. cogmem defaults ON; maestro defaults OFF.
func (c *Config) AgentSuiteEnabled(agentID, suite string) bool {
	id := strings.TrimSpace(agentID)
	for i := range c.Agents.List {
		if strings.EqualFold(c.Agents.List[i].ID, id) {
			a := &c.Agents.List[i]
			switch suite {
			case "maestro":
				return a.Maestro
			case "cogmem":
				return a.CognitiveMemoryEnabled()
			default:
				return false
			}
		}
	}
	// Unknown agent: fall back to the suite default (cogmem on, others off).
	return suite == "cogmem"
}

// CronTarget resolves the agent's default-channel delivery coordinates from its
// default binding. ok is false when the agent has no default binding or that
// binding does not resolve to a concrete chat. The values are the binding's own
// Match fields, so delivering to them routes straight back to the agent.
func (c *Config) CronTarget(agentID string) (channel, chatID, peerKind string, ok bool) {
	b, found := c.DefaultBinding(agentID)
	if !found || b.Match.Channel == "" {
		return "", "", "", false
	}
	// A concrete routing peer (e.g. a Slack channel binding) is the delivery target.
	if b.Match.Peer != nil && b.Match.Peer.Kind != "" && b.Match.Peer.ID != "" {
		return b.Match.Channel, b.Match.Peer.ID, b.Match.Peer.Kind, true
	}
	// Otherwise use the explicit delivery target (e.g. a Telegram chat id on a
	// broadly-bound bot). This does not affect routing.
	if b.DeliverTo != "" {
		kind := b.DeliverPeerKind
		if kind == "" {
			kind = "direct"
		}
		return b.Match.Channel, b.DeliverTo, kind, true
	}
	return "", "", "", false
}

// ValidateBindings rejects an inconsistent binding set: more than one default
// per agent, or a default binding that does not resolve to a concrete chat
// (needs Match.Channel + Match.Peer{Kind,ID}) and so could not receive cron output.
func (c *Config) ValidateBindings() error {
	defaults := map[string]int{}
	for i := range c.Bindings {
		b := &c.Bindings[i]
		if !b.Default {
			continue
		}
		defaults[b.AgentID]++
		if defaults[b.AgentID] > 1 {
			return fmt.Errorf("agent %q has more than one default binding", b.AgentID)
		}
		concretePeer := b.Match.Peer != nil && b.Match.Peer.Kind != "" && b.Match.Peer.ID != ""
		if b.Match.Channel == "" || (!concretePeer && b.DeliverTo == "") {
			return fmt.Errorf("default binding for agent %q must resolve to a concrete chat: either a peer (kind+id) or a deliver_to chat id", b.AgentID)
		}
	}
	return nil
}

type AgentDefaults struct {
	RestrictToWorkspace bool `json:"restrict_to_workspace"           env:"CLAW_AGENTS_DEFAULTS_RESTRICT_TO_WORKSPACE"`
	// StreamToolActivity, when true, sends the model's inter-tool narration and
	// each tool's user-facing output to the channel as it happens. When false
	// (default) the user receives only the final answer, not the play-by-play.
	StreamToolActivity        bool `json:"stream_tool_activity,omitempty"  env:"CLAW_AGENTS_DEFAULTS_STREAM_TOOL_ACTIVITY"`
	AllowReadOutsideWorkspace bool `json:"allow_read_outside_workspace"    env:"CLAW_AGENTS_DEFAULTS_ALLOW_READ_OUTSIDE_WORKSPACE"`
	// ShowReasoningAsContent, when true, lets a model's reasoning_content be used
	// as the user-facing reply when the model returns empty content. Default false:
	// reasoning never reaches the main chat (it would otherwise leak raw
	// chain-of-thought, e.g. a model that degenerates into reasoning-only output).
	ShowReasoningAsContent bool `json:"show_reasoning_as_content,omitempty" env:"CLAW_AGENTS_DEFAULTS_SHOW_REASONING_AS_CONTENT"`
	// WorkspaceWriteSubdir confines writes to <workspace>/<subdir> while reads
	// remain workspace-wide. Only applies when RestrictToWorkspace is true.
	// Default "files" (writes land in <workspace>/files). Set to "" to make the
	// whole workspace writable (legacy behavior).
	WorkspaceWriteSubdir string `json:"workspace_write_subdir"          env:"CLAW_AGENTS_DEFAULTS_WORKSPACE_WRITE_SUBDIR"`
	// WorkspaceReadSubdirs confines agent file reads to these <workspace>/<subdir>
	// directories (plus allow-listed host paths). Only applies when
	// RestrictToWorkspace is true. Default ["files","skills"] — the agent's
	// read/write area plus its skills. (The sub-agent task-results dir, tasks/, is
	// always readable regardless of this setting — spawn callbacks point the agent
	// at it; see the files tool provider.) Empty makes reads workspace-wide
	// (legacy), which exposes config/subsystem files (AGENTS.md, COGMEM.md, …) the
	// agent already receives in its prompt or should never read.
	WorkspaceReadSubdirs []string `json:"workspace_read_subdirs"          env:"CLAW_AGENTS_DEFAULTS_WORKSPACE_READ_SUBDIRS"`
	Models               []string `json:"models,omitempty"`
	ImageModel           string   `json:"image_model,omitempty"           env:"CLAW_AGENTS_DEFAULTS_IMAGE_MODEL"`
	ImageModelFallbacks  []string `json:"image_model_fallbacks,omitempty"`
	// RequestTimeout is the global default request timeout (seconds) applied to
	// any model whose own request_timeout is 0. Default 300; CLI models override
	// it higher (e.g. 3600). 0 falls back to the built-in 120s HTTP default.
	RequestTimeout int `json:"request_timeout,omitempty"       env:"CLAW_AGENTS_DEFAULTS_REQUEST_TIMEOUT"`
	// TurnTimeout is the overall wall-clock budget (seconds) for a single user
	// turn — all LLM iterations plus every tool call. It is a hard backstop that
	// guarantees the turn ends (the context is cancelled) so the user always gets
	// a reply and the typing indicator clears, even if a provider or tool hangs.
	// 0 falls back to the built-in default (DefaultTurnTimeout).
	TurnTimeout int `json:"turn_timeout,omitempty"          env:"CLAW_AGENTS_DEFAULTS_TURN_TIMEOUT"`
	// ToolTimeout is the per-tool-call budget (seconds). A tool whose context
	// deadline elapses is cancelled and reported as a timeout to the model, which
	// can then continue the turn. 0 falls back to DefaultToolTimeout.
	ToolTimeout int `json:"tool_timeout,omitempty"          env:"CLAW_AGENTS_DEFAULTS_TOOL_TIMEOUT"`
	// ProgressInterval is how often (seconds) a long-running turn emits a
	// lightweight progress update so it never looks dead. 0 falls back to
	// DefaultProgressInterval; a negative value disables progress updates.
	ProgressInterval           int      `json:"progress_interval,omitempty"     env:"CLAW_AGENTS_DEFAULTS_PROGRESS_INTERVAL"`
	MaxTokens                  int      `json:"max_tokens"                      env:"CLAW_AGENTS_DEFAULTS_MAX_TOKENS"`
	Temperature                *float64 `json:"temperature,omitempty"           env:"CLAW_AGENTS_DEFAULTS_TEMPERATURE"`
	MaxToolIterations          int      `json:"max_tool_iterations"             env:"CLAW_AGENTS_DEFAULTS_MAX_TOOL_ITERATIONS"`
	ContextWindow              int      `json:"context_window,omitempty"        env:"CLAW_AGENTS_DEFAULTS_CONTEXT_WINDOW"`
	MaxMediaSize               int      `json:"max_media_size,omitempty"        env:"CLAW_AGENTS_DEFAULTS_MAX_MEDIA_SIZE"`
	CompressMinPercent         int      `json:"compress_min_percent,omitempty"          env:"CLAW_AGENTS_DEFAULTS_COMPRESS_MIN_PERCENT"`
	CompressNormalPercent      int      `json:"compress_normal_percent,omitempty"       env:"CLAW_AGENTS_DEFAULTS_COMPRESS_NORMAL_PERCENT"`
	CompressSafetyPercent      int      `json:"compress_safety_percent,omitempty"       env:"CLAW_AGENTS_DEFAULTS_COMPRESS_SAFETY_PERCENT"`
	CompressMessageThreshold   int      `json:"compress_message_threshold,omitempty"    env:"CLAW_AGENTS_DEFAULTS_COMPRESS_MESSAGE_THRESHOLD"`
	CompressRetainTokenPercent int      `json:"compress_retain_token_percent,omitempty" env:"CLAW_AGENTS_DEFAULTS_COMPRESS_RETAIN_TOKEN_PERCENT"`
	CompressRetainMinMessages  int      `json:"compress_retain_min_messages,omitempty"  env:"CLAW_AGENTS_DEFAULTS_COMPRESS_RETAIN_MIN_MESSAGES"`
	CompressCharsPerToken      float64  `json:"compress_chars_per_token,omitempty"      env:"CLAW_AGENTS_DEFAULTS_COMPRESS_CHARS_PER_TOKEN"`
	CompressTokenSafetyMargin  float64  `json:"compress_token_safety_margin,omitempty"  env:"CLAW_AGENTS_DEFAULTS_COMPRESS_TOKEN_SAFETY_MARGIN"`
	ArchiveMessageCount        int      `json:"archive_message_count,omitempty"         env:"CLAW_AGENTS_DEFAULTS_ARCHIVE_MESSAGE_COUNT"`
	ArchiveDays                int      `json:"archive_days,omitempty"                  env:"CLAW_AGENTS_DEFAULTS_ARCHIVE_DAYS"`
	SummaryMaxCount            int      `json:"summary_max_count,omitempty"             env:"CLAW_AGENTS_DEFAULTS_SUMMARY_MAX_COUNT"`
	SummaryRetentionDays       int      `json:"summary_retention_days,omitempty"        env:"CLAW_AGENTS_DEFAULTS_SUMMARY_RETENTION_DAYS"`
	ArchiveContentMaxBytes     int      `json:"archive_content_max_bytes,omitempty"     env:"CLAW_AGENTS_DEFAULTS_ARCHIVE_CONTENT_MAX_BYTES"`
	DefaultTools               []string `json:"default_tools,omitempty"`

	// ContextEviction is the default per-turn tool-result eviction policy
	// (overridable per agent via AgentConfig.ContextEviction).
	ContextEviction *ContextEvictionConfig `json:"context_eviction,omitempty"`

	// Memory is the default cognitive-memory config applied to agents allowed
	// the cogmem tools (overridable per agent via AgentConfig.Memory).
	Memory MemoryConfig `json:"memory"`
}

// EffectiveMemory returns the memory config for an agent: the per-agent block
// if present, otherwise the defaults.
func (d AgentDefaults) EffectiveMemory(a *AgentConfig) MemoryConfig {
	if a != nil && a.Memory != nil {
		return *a.Memory
	}
	return d.Memory
}

const DefaultMaxMediaSize = 20 * 1024 * 1024 // 20 MB

func (d *AgentDefaults) GetMaxMediaSize() int {
	if d.MaxMediaSize > 0 {
		return d.MaxMediaSize
	}
	return DefaultMaxMediaSize
}

// Turn/tool/progress defaults. A turn is the whole exchange for one user
// message (all LLM iterations + tool calls); the turn budget is the hard
// backstop against a hung provider or tool.
const (
	DefaultTurnTimeout      = 15 * time.Minute
	DefaultToolTimeout      = 5 * time.Minute
	DefaultProgressInterval = 30 * time.Second
)

// GetTurnTimeout returns the overall turn budget: the configured value (seconds)
// or DefaultTurnTimeout when unset (0).
func (d *AgentDefaults) GetTurnTimeout() time.Duration {
	if d.TurnTimeout > 0 {
		return time.Duration(d.TurnTimeout) * time.Second
	}
	return DefaultTurnTimeout
}

// GetToolTimeout returns the per-tool-call budget: the configured value
// (seconds) or DefaultToolTimeout when unset (0).
func (d *AgentDefaults) GetToolTimeout() time.Duration {
	if d.ToolTimeout > 0 {
		return time.Duration(d.ToolTimeout) * time.Second
	}
	return DefaultToolTimeout
}

// GetProgressInterval returns the progress-update cadence: the configured value
// (seconds), DefaultProgressInterval when unset (0), or 0 (disabled) when
// negative.
func (d *AgentDefaults) GetProgressInterval() time.Duration {
	if d.ProgressInterval < 0 {
		return 0
	}
	if d.ProgressInterval == 0 {
		return DefaultProgressInterval
	}
	return time.Duration(d.ProgressInterval) * time.Second
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

// Cooldown category defaults (minutes). Billing/auth is long because the operator
// usually has to top up or rotate a key; the rest are short.
const (
	DefaultCooldownBillingAuthMinutes = 60
	DefaultCooldownRateLimitMinutes   = 10
	DefaultCooldownBadRequestMinutes  = 1
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

// DefaultModelName returns the first model in the list, or "" if unset.
func (d *AgentDefaults) DefaultModelName() string {
	if len(d.Models) == 0 {
		return ""
	}
	return d.Models[0]
}

// SetDefaultModel makes modelName the first entry in the model list,
// preserving any existing remaining entries.
func (d *AgentDefaults) SetDefaultModel(modelName string) {
	if len(d.Models) == 0 {
		d.Models = []string{modelName}
	} else {
		d.Models[0] = modelName
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
	Name     string `json:"name"`     // Unique identifier referenced by ModelConfig.Provider
	Protocol string `json:"protocol"` // Wire format: openai, anthropic, anthropic-messages, azure, claude-cli, codex-cli, gemini-cli
	BaseURL  string `json:"base_url,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	Proxy    string `json:"proxy,omitempty"`
	// Endpoint-scoped openai-compat knobs.
	StrictCompat        bool `json:"strict_compat,omitempty"`
	NoParallelToolCalls bool `json:"no_parallel_tool_calls,omitempty"`
	ResponseFormatJSON  bool `json:"response_format_json,omitempty"`
	// Command overrides the binary path for CLI protocols (claude-cli, etc.).
	Command string `json:"command,omitempty"`
}

// ModelConfig represents a model-centric provider configuration.
// It allows adding new providers (especially OpenAI-compatible ones) via configuration only.
// The model field uses protocol prefix format: [protocol/]model-identifier
// Supported protocols: openai, anthropic, claude-cli, codex-cli
// Default protocol is "openai" if no prefix is specified.
type ModelConfig struct {
	// Required fields
	ModelName string `json:"model_name"` // User-facing alias for the model
	Model     string `json:"model"`      // Raw model id the endpoint expects (no claw protocol prefix)
	Provider  string `json:"provider"`   // Name of the Provider this model is reached through

	// Special providers (CLI-based, OAuth, etc.)
	ConnectMode string `json:"connect_mode,omitempty"` // Connection mode: stdio, grpc
	Workspace   string `json:"workspace,omitempty"`    // Workspace path for CLI-based providers

	// Optional optimizations
	RPM            int               `json:"rpm,omitempty"`              // Requests per minute limit
	MaxTokens      int               `json:"max_tokens,omitempty"`       // Maximum tokens per response; overrides agent defaults
	ContextWindow  int               `json:"context_window,omitempty"`   // Actual model context window size in tokens
	MaxTokensField string            `json:"max_tokens_field,omitempty"` // Field name for max tokens (e.g., "max_completion_tokens")
	RequestTimeout int               `json:"request_timeout,omitempty"`
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
	// models that natively accept it (notably Grok). Valid values are "none",
	// "low", "medium", "high", or empty. Empty omits the field entirely; "none"
	// is sent explicitly (e.g. to disable reasoning on models that support it).
	// Providers that don't understand the field will silently ignore it.
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
		return fmt.Errorf("model_name is required")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required")
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
	// AutoEnable, when true (the default), connects to external MCP servers
	// automatically whenever at least one configured server is enabled — so a
	// user need not also flip the master `enabled` flag. Explicit Enabled=true
	// always wins. Mirrors MCPHostConfig.AutoEnable.
	AutoEnable bool `json:"auto_enable" env:"CLAW_TOOLS_MCP_AUTO_ENABLE"`
	// Servers is a map of server name to server configuration
	Servers map[string]MCPServerConfig `json:"servers,omitempty"`
}

// MCPClientEffectivelyEnabled reports whether claw should connect out to external
// MCP servers: the master flag wins, otherwise auto-enable kicks in when any
// configured server is enabled.
func (t *ToolsConfig) MCPClientEffectivelyEnabled() bool {
	if t.MCP.Enabled {
		return true
	}
	if !t.MCP.AutoEnable {
		return false
	}
	for _, s := range t.MCP.Servers {
		if s.Enabled {
			return true
		}
	}
	return false
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
	// Supports "*" (all), prefix globs like "read_*", and exact names. Every
	// tool obeys the allowlist; nothing (including msg_send) is hard-excluded.
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

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	warnLegacyCompressModel(data)

	if err := env.Parse(cfg); err != nil {
		return nil, err
	}

	// Migrate legacy channel config fields to new unified structures
	cfg.migrateChannelConfigs()

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
// <agents base>/common.
func (c *Config) ResolveCommonDir() string {
	if c.Agents.CommonDir != "" {
		return expandHome(c.Agents.CommonDir)
	}
	return filepath.Join(c.BaseDir(), "common")
}

// WorkspacePath returns the primary/default-agent workspace (<base_dir>/default).
// It is used for gateway-global operations (skills view, gateway state, MCP
// config) and as the CLI-provider working-dir fallback. Per-agent workspaces are
// resolved by pkg/agent.resolveAgentWorkspace.
func (c *Config) WorkspacePath() string {
	return filepath.Join(c.BaseDir(), "default")
}

// AgentSessionDirs returns the sessions subdirectory for every configured
// agent, deduped. This mirrors the workspace resolution logic in
// pkg/agent/instance.go:resolveAgentWorkspace. The result is used by the
// WebUI to enumerate sessions across all agents, not just the defaults workspace.
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

	// Always include the default workspace — covers agents that were removed
	// from config but left files on disk.
	add(filepath.Join(base, "default"))

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

// BackupConfig controls the nightly configuration backup of key files
// (config.json and the cron jobs file) into <data dir>/backup/YYYYMMDD/.
type BackupConfig struct {
	// Enabled is on by default: nil (field absent) means enabled. Set an explicit
	// false to turn the nightly backup off.
	Enabled    *bool  `json:"enabled,omitempty"`
	At         string `json:"at,omitempty"`          // "HH:MM" local time; default 03:00
	RetainDays int    `json:"retain_days,omitempty"` // prune older day-folders; default 30
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
// to an internal provider implementation in pkg/providers.
var validProtocols = map[string]struct{}{
	"openai-chat":        {},
	"openai-responses":   {},
	"azure":              {},
	"anthropic":          {},
	"anthropic-messages": {},
	"claude-cli":         {},
	"codex-cli":          {},
	"gemini-cli":         {},
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
	case "claude-cli", "codex-cli", "gemini-cli":
		return true
	default:
		return false
	}
}

// HasCredentials reports whether this provider carries enough to authenticate:
// CLI providers always qualify (they auth out-of-band); HTTP providers need an
// API key.
func (p *Provider) HasCredentials() bool {
	if IsCLIProtocol(p.Protocol) {
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
	renameInSlice(c.Agents.Defaults.Models, oldName, newName)
	c.Agents.Defaults.ImageModel = renameScalar(c.Agents.Defaults.ImageModel, oldName, newName)
	renameInSlice(c.Agents.Defaults.ImageModelFallbacks, oldName, newName)
	renameInSlice(c.Summarization.Models, oldName, newName)
	for i := range c.Agents.List {
		renameInSlice(c.Agents.List[i].Models, oldName, newName)
		renameInSlice(c.Agents.List[i].SummarizationModels, oldName, newName)
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
		return fmt.Errorf("provider name is required")
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
