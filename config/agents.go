package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// AgentMentionConfig controls how agent names are extracted from message content.
type AgentMentionConfig struct {
	// Triggers is the set of prefix characters that introduce an agent mention.
	// Defaults to ["@", "/", "."] when empty.
	Triggers []string `json:"triggers,omitempty"`
}

type AgentsConfig struct {
	// BaseDir is the base directory under which every agent's workspace lives:
	// each agent resolves to <base_dir>/<agent-id> (an agent with an empty id
	// or the id "main" uses <base_dir>/default). A per-agent `workspace`
	// overrides this. Empty
	// defaults to <data_dir>/agents. Point it at another volume to relocate all
	// agent files at once.
	BaseDir string `json:"base_dir,omitempty" env:"CLAW_AGENTS_BASE_DIR"`
	// CommonDir is the global path to the shared directory that agents can read
	// from and write to via the "common" tools. Empty defaults to
	// <data_dir>/common (see Config.ResolveCommonDir).
	CommonDir string        `json:"common_dir,omitempty" env:"CLAW_AGENTS_COMMON_DIR"`
	Defaults  AgentDefaults `json:"defaults"`
	// List is never omitted: LoadConfig fills an absent list with the built-in
	// default agent, so an emptied list is written as [] (see MarshalJSON).
	List []AgentConfig `json:"list"`
}

// MarshalJSON writes an empty agent list as [], never null; see List.
func (a AgentsConfig) MarshalJSON() ([]byte, error) {
	type Alias AgentsConfig
	aux := struct {
		List []AgentConfig `json:"list"`
		Alias
	}{
		List:  nonNilSlice(a.List),
		Alias: Alias(a),
	}
	return json.Marshal(aux)
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

	// Maestro configures the Maestro task-orchestration tool suite (projects,
	// playbooks, tasks) for this agent. Absent or disabled means no Maestro
	// tools. When enabled, the agent gets the entire Maestro toolset, with
	// per-agent data under <workspace>/maestro, and that directory is
	// auto-mounted read/write for native file_* tools as maestro/ (unless the
	// agent already defines a mount named "maestro"). The runner settings inside
	// the block are passed to Maestro; unset ones take Maestro's defaults.
	Maestro *MaestroConfig `json:"maestro,omitempty"`

	// Fusion switches on the MCPFusion config-driven REST-API tool suite for the
	// agent; which services it then gets is decided by MCPTools (see
	// FusionToolAllowed). It remains the master switch for the MCPFusion
	// tool suite. Off by default. When on, the agent gets every tool defined by the
	// JSON config files under <dataDir>/fusion, with per-agent OAuth tokens keyed by
	// agent id in the shared fusion token store.
	Fusion bool `json:"fusion,omitempty"`

	// Forum lets the agent set up and manage forums with other agents: the
	// forum_* tool suite (the forum package), gated as a unit. Off by default.
	// A forum's files live under <workspace>/forums. Turning it off removes the
	// tools; forums already running continue.
	Forum bool `json:"forum,omitempty"`

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

	// Compression is the per-agent context-compaction policy, overriding the
	// defaults block field by field. See CompressionConfig.
	Compression *CompressionConfig `json:"compression,omitempty"`

	// Legacy flat compress_* keys. Retained ONLY as an inlet for
	// migrateCompressionConfigs, which folds them into Compression at load time
	// and clears them; nothing else in the codebase reads them. Delete once
	// deployed configs have been rewritten.
	CompressMinPercent         *int     `json:"compress_min_percent,omitempty"`
	CompressNormalPercent      *int     `json:"compress_normal_percent,omitempty"`
	CompressSafetyPercent      *int     `json:"compress_safety_percent,omitempty"`
	CompressMessageThreshold   *int     `json:"compress_message_threshold,omitempty"`
	CompressRetainTokenPercent *int     `json:"compress_retain_token_percent,omitempty"`
	CompressRetainMinMessages  *int     `json:"compress_retain_min_messages,omitempty"`
	CompressCharsPerToken      *float64 `json:"compress_chars_per_token,omitempty"`
	CompressTokenSafetyMargin  *float64 `json:"compress_token_safety_margin,omitempty"`

	ArchiveMessageCount  *int `json:"archive_message_count,omitempty"`
	ArchiveDays          *int `json:"archive_days,omitempty"`
	SummaryMaxCount      *int `json:"summary_max_count,omitempty"`
	SummaryRetentionDays *int `json:"summary_retention_days,omitempty"`

	// EventRetentionDays and RetiredRetentionDays override the memory retention
	// windows for this agent alone. nil uses agents.defaults; 0 means the
	// built-in default; negative keeps forever.
	//
	// Scalars here rather than inside Memory because AgentConfig.Memory
	// overrides the defaults WHOLESALE — setting retention through it would
	// silently zero this agent's prompt budgets. They are applied on top of the
	// resolved MemoryConfig by EffectiveMemory.
	EventRetentionDays     *int `json:"event_retention_days,omitempty"`
	RetiredRetentionDays   *int `json:"retired_retention_days,omitempty"`
	ArchiveContentMaxBytes *int `json:"archive_content_max_bytes,omitempty"`

	// ContextEviction overrides the per-turn tool-result eviction policy for
	// this agent. Unset fields fall back to the defaults block, then to the
	// built-in defaults.
	ContextEviction *ContextEvictionConfig `json:"context_eviction,omitempty"`

	// Mounts expose external directory trees as top-level names in this agent's
	// space (peers of files/ and skills/), accessed as <name>/... Per agent.
	Mounts []MountConfig `json:"mounts,omitempty"`

	// MCPTools is the per-agent allow-list for external MCP-client tools and,
	// when Fusion is on, for Fusion services. It is kept separate from the
	// generic Tools allowlist so this access is per server or service rather
	// than all-or-nothing. Each entry is matched (case-insensitively) against a
	// tool's <server>_<tool> name (the published mcp_<server>_<tool> with the
	// mcp_ prefix stripped) or a Fusion tool's <service>_<tool> name: an entry
	// allows the tool when it equals or is a prefix of that name. So "github"
	// admits every tool on the github server, "wxca" every tool of the wxca
	// Fusion service, "microsoft365_calendar" one group of the microsoft365
	// service. Empty ⇒ the agent gets no MCP tools and, Fusion on or off, no
	// Fusion tools. The mcp_ prefix and wildcards are never needed.
	MCPTools []string `json:"mcp_tools,omitempty"`

	// DenyTools lists tools this agent may never call, even when Tools or
	// MCPTools admits them. It is evaluated after the allow lists and deny wins.
	// Each entry uses the matching rule of the list the tool belongs to: a
	// generic tool is matched under MatchToolPattern (case-insensitive exact, or
	// prefix with a trailing "*"), an external MCP tool under MCPToolAllowed's
	// rule (mcp_ stripped, underscore runs collapsed, equality-or-prefix). So
	// "shell_exec" blocks that local tool and "google_calendar_event_delete"
	// blocks that MCP tool under a broader "google" grant. Empty ⇒ no denials.
	DenyTools []string `json:"deny_tools,omitempty"`
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
	// Writable opens the mount for writing. It defaults to false (read-only), so
	// an agent can only modify a mounted folder when write access is explicitly
	// granted; read-only mounts reject every write/delete.
	Writable bool `json:"writable,omitempty"`
}

var mountNameRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// ValidateMountName checks a mount name: a single path component of letters,
// digits, and hyphens, not one of ReservedWorkspaceNames (in any case).
func ValidateMountName(name string) error {
	if !mountNameRe.MatchString(name) {
		return fmt.Errorf("mount name %q: use only letters, digits, and '-' (a single directory name)", name)
	}
	if IsReservedWorkspaceName(name) {
		return fmt.Errorf("mount name %q is reserved", name)
	}
	return nil
}

// MaestroMountName is the top-level file-tool path for Maestro data when the
// suite is enabled (peer of files/ and skills/).
const MaestroMountName = "maestro"

// MaestroDataDir is the on-disk Maestro tree for an agent workspace.
func MaestroDataDir(workspace string) string {
	return filepath.Join(workspace, MaestroMountName)
}

// MaestroConfig is the per-agent Maestro block. It replaced the earlier boolean
// `"maestro": true`; that form is not honoured (see UnmarshalJSON).
type MaestroConfig struct {
	// Enabled turns the Maestro tool suite on for the agent.
	Enabled bool `json:"enabled"`
	// MaxConcurrent caps how many tasks a parallel task-set run executes at
	// once. 0 = Maestro's default (5).
	MaxConcurrent int `json:"max_concurrent,omitempty"`
	// RateLimitRequests and RateLimitPeriod bound task dispatches to at most
	// RateLimitRequests per RateLimitPeriod seconds. 0 = Maestro's defaults
	// (10 per 60 s).
	RateLimitRequests int `json:"rate_limit_requests,omitempty"`
	RateLimitPeriod   int `json:"rate_limit_period,omitempty"`
	// AllowParallel controls whether a parallel run may be honoured when the
	// LLM asks for one. Parallel execution is never the default: it must be
	// requested per run. nil or true allows it; false forces sequential runs.
	AllowParallel *bool `json:"allow_parallel,omitempty"`

	// legacyBool records that the config carried the retired boolean form.
	legacyBool bool
}

// UnmarshalJSON accepts the object form. The retired boolean form is parsed
// without error but not honoured: Maestro stays disabled for that agent and
// LoadConfig logs a warning, so a stale config does not stop the gateway.
func (m *MaestroConfig) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*m = MaestroConfig{}
		return nil
	}
	if trimmed[0] != '{' {
		*m = MaestroConfig{legacyBool: true}
		return nil
	}
	type plain MaestroConfig
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*m = MaestroConfig(p)
	return nil
}

// LegacyBoolean reports whether the config carried the retired boolean form.
func (m *MaestroConfig) LegacyBoolean() bool { return m != nil && m.legacyBool }

// ParallelAllowed reports whether a requested parallel run may be honoured.
func (m *MaestroConfig) ParallelAllowed() bool {
	return m == nil || m.AllowParallel == nil || *m.AllowParallel
}

// MaestroEnabled reports whether the Maestro suite is on for this agent.
func (a *AgentConfig) MaestroEnabled() bool {
	return a != nil && a.Maestro != nil && a.Maestro.Enabled
}

// EffectiveMounts returns the agent's configured mounts, less any whose name
// is reserved (IgnoredMounts: the workspace folder wins), plus an
// auto-injected read/write mount of <workspace>/maestro when Maestro is
// enabled. The returned Path for the auto mount is absolute when workspace is
// non-empty. Does not create the directory — the files provider does that
// when installing mounts.
func (a *AgentConfig) EffectiveMounts(workspace string) []MountConfig {
	if a == nil {
		return nil
	}
	var out []MountConfig
	for _, m := range a.Mounts {
		if !IsReservedWorkspaceName(m.Name) {
			out = append(out, m)
		}
	}
	if !a.MaestroEnabled() || strings.TrimSpace(workspace) == "" {
		return out
	}
	abs := MaestroDataDir(workspace)
	if a, err := filepath.Abs(abs); err == nil {
		abs = a
	}
	return append(out, MountConfig{
		Name:     MaestroMountName,
		Path:     abs,
		Writable: true,
	})
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
		if stem, ok := strings.CutSuffix(entry, "*"); ok {
			prefix := strings.ToLower(stem)
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
//
// External MCP-client tools (mcp_<server>_<tool>) are gated SOLELY by the
// dedicated mcp_tools list, never the generic Tools allowlist — so this is the
// single source of truth shared by both the registration gate and the
// execution-time defense-in-depth check (they must agree, or a tool can be
// registered yet rejected on call).
//
// DenyTools is applied after the allow list, with the same matching rule as
// that list; a denied tool is never allowed.
func (a *AgentConfig) IsToolAllowed(name string) bool {
	if a == nil {
		return false
	}
	if strings.HasPrefix(strings.ToLower(name), "mcp_") {
		return a.MCPToolAllowed(name)
	}
	// nil Tools (key absent in config) → use install defaults.
	// Empty Tools (tools: [] in config) → deny all intentionally.
	allow := a.Tools
	if allow == nil {
		allow = DefaultAgentTools
	}
	// shell_exec is granted only by naming it: no wildcard or prefix entry
	// includes it, and no install-wide setting turns it on or off.
	if IsShellTool(name) {
		return namesTool(allow, name) && !a.IsToolDenied(name)
	}
	// DenyTools is checked after the allow list, under the same rule; deny wins.
	return MatchToolPattern(allow, name) && !a.IsToolDenied(name)
}

// ShellExecTool is the published name of the shell tool. An agent runs shell
// commands only when its own tools list names it exactly ("Allow shell
// commands" in the WebUI); a "*" or prefix entry never includes it.
const ShellExecTool = "shell_exec"

// IsShellTool reports whether name, in any case, is the shell tool.
func IsShellTool(name string) bool {
	return strings.EqualFold(name, ShellExecTool)
}

// namesTool reports whether patterns names the tool exactly
// (case-insensitive), ignoring wildcard and prefix entries.
func namesTool(patterns []string, name string) bool {
	for _, entry := range patterns {
		if strings.EqualFold(strings.TrimSpace(entry), name) {
			return true
		}
	}
	return false
}

// IsToolDenied reports whether deny_tools names the tool, independent of any
// allow list or suite grant. It is the deny-only check for tools that are not
// gated per tool on the allow side (suite tools: Fusion, Maestro, cogmem, the
// discovery meta tools), so an operator's explicit deny wins however the tool
// arrived. An mcp_ name is matched under MCPToolAllowed's rule, anything else
// under MatchToolPattern (case-insensitive exact, or prefix with a trailing
// "*"). A nil agent or empty DenyTools denies nothing.
func (a *AgentConfig) IsToolDenied(name string) bool {
	if a == nil || len(a.DenyTools) == 0 {
		return false
	}
	if strings.HasPrefix(strings.ToLower(name), "mcp_") {
		bare := strings.ToLower(strings.TrimPrefix(mcpUnderscoreRun.ReplaceAllString(name, "_"), "mcp_"))
		return matchMCPEntry(a.DenyTools, bare)
	}
	return MatchToolPattern(a.DenyTools, name)
}

// mcpUnderscoreRun collapses any run of 2+ underscores to a single one before
// MCP allow-list comparison, so a server/tool join that yields mcp_fusion__tool
// (or a published mcp__fusion_…) still matches a clean entry like "fusion_tool".
var mcpUnderscoreRun = regexp.MustCompile(`_{2,}`)

// MCPToolAllowed reports whether an external MCP-client tool is permitted for
// this agent. name is the published tool name (mcp_<server>_<tool>); underscore
// runs are collapsed, the mcp_ prefix is stripped, and the remaining
// <server>_<tool> is matched (case-insensitively) against each MCPTools entry:
// an entry admits the tool when it equals or is a prefix of that name. An empty
// MCPTools list admits nothing. Unlike the generic tools allowlist, no wildcard
// or mcp_ prefix is used. DenyTools entries are matched by the same rule after
// the allow list, and a denied tool is never allowed.
func (a *AgentConfig) MCPToolAllowed(name string) bool {
	if a == nil || len(a.MCPTools) == 0 {
		return false
	}
	// Collapse underscores on the full name first, THEN strip mcp_, so a doubled
	// prefix (mcp__…) reduces to a single mcp_ before stripping.
	bare := strings.ToLower(strings.TrimPrefix(mcpUnderscoreRun.ReplaceAllString(name, "_"), "mcp_"))
	// DenyTools is checked after the allow list, under the same rule; deny wins.
	return matchMCPEntry(a.MCPTools, bare) && !matchMCPEntry(a.DenyTools, bare)
}

// FusionToolAllowed reports whether a Fusion tool (named <service>_<tool>, no
// mcp_ prefix) is granted to this agent: the Fusion suite must be on and an
// MCPTools entry must equal or prefix the name under matchMCPEntry's rule, so
// "wxca" admits every wxca tool and "microsoft365_calendar" one group of the
// microsoft365 service. Fusion on with no matching entry admits nothing.
// DenyTools is applied by the caller through IsToolDenied, as for every suite
// tool.
func (a *AgentConfig) FusionToolAllowed(name string) bool {
	if a == nil || !a.Fusion || len(a.MCPTools) == 0 {
		return false
	}
	return matchMCPEntry(a.MCPTools, mcpUnderscoreRun.ReplaceAllString(strings.ToLower(name), "_"))
}

// matchMCPEntry reports whether any entry equals or is a prefix of bare (an
// MCP tool's lowercased <server>_<tool> name with underscore runs collapsed).
// Entries are lowercased, trimmed and underscore-collapsed; blank ones are
// skipped. Empty entries matches nothing.
func matchMCPEntry(entries []string, bare string) bool {
	for _, entry := range entries {
		e := mcpUnderscoreRun.ReplaceAllString(strings.ToLower(strings.TrimSpace(entry)), "_")
		if e == "" {
			continue
		}
		if strings.HasPrefix(bare, e) {
			return true
		}
	}
	return false
}

// MatchVisibility reports whether a tool named `name` passes a coarse MCP-host
// visibility filter (the per-endpoint InternalTools/ExternalTools lists). It uses
// the same ergonomics as the per-agent MCP allow-list, generalized to local tools
// too: underscores are collapsed, a leading mcp_ is stripped, and an entry admits
// the tool when it equals or is a prefix of the result (case-insensitive). A "*"
// entry exposes everything; an empty list exposes nothing. So "file" or
// "session_info" match local tools, and "fusion"/"fusion_wxca" match upstream MCP
// tools without the mcp_ prefix or a glob.
func MatchVisibility(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return false
	}
	bare := strings.TrimPrefix(mcpUnderscoreRun.ReplaceAllString(strings.ToLower(name), "_"), "mcp_")
	for _, entry := range patterns {
		e := strings.TrimSpace(strings.ToLower(entry))
		if e == "*" {
			return true
		}
		// Tolerate a trailing glob so "fusion_*" behaves the same as "fusion_".
		e = mcpUnderscoreRun.ReplaceAllString(strings.TrimSuffix(e, "*"), "_")
		if e == "" {
			continue
		}
		if strings.HasPrefix(bare, e) {
			return true
		}
	}
	return false
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
	// RetentionDays deletes a session's archive database once its last
	// activity is older than this many days (checked nightly). 0 keeps every
	// session forever. An agent's main session is never deleted.
	RetentionDays int `json:"retention_days,omitempty"`
}

// DefaultBinding returns the agent's binding marked Default, or false if none.
// Agent ids are compared by identity (SameAgentID).
func (c *Config) DefaultBinding(agentID string) (*AgentBinding, bool) {
	for i := range c.Bindings {
		b := &c.Bindings[i]
		if b.Default && SameAgentID(b.AgentID, agentID) {
			return b, true
		}
	}
	return nil, false
}

// AgentHasGlobalCron reports whether the agent may schedule/manage cron jobs for
// other agents.
func (c *Config) AgentHasGlobalCron(agentID string) bool {
	for i := range c.Agents.List {
		if SameAgentID(c.Agents.List[i].ID, agentID) {
			return c.Agents.List[i].GlobalCron
		}
	}
	return false
}

// AgentHasMaestro reports whether the agent has the Maestro tool suite enabled.
func (c *Config) AgentHasMaestro(agentID string) bool {
	for i := range c.Agents.List {
		if SameAgentID(c.Agents.List[i].ID, agentID) {
			return c.Agents.List[i].MaestroEnabled()
		}
	}
	return false
}

// AgentMaestro returns the agent's Maestro block, or nil when the agent is
// unknown or has no block.
func (c *Config) AgentMaestro(agentID string) *MaestroConfig {
	for i := range c.Agents.List {
		if SameAgentID(c.Agents.List[i].ID, agentID) {
			return c.Agents.List[i].Maestro
		}
	}
	return nil
}

// AgentByID returns the agent's config, or nil when unknown.
func (c *Config) AgentByID(agentID string) *AgentConfig {
	for i := range c.Agents.List {
		if SameAgentID(c.Agents.List[i].ID, agentID) {
			return &c.Agents.List[i]
		}
	}
	return nil
}

// FindAgent returns the configured agent whose id, or else whose name, equals
// ref (case-insensitively), or nil.
func (c *Config) FindAgent(ref string) *AgentConfig {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if a := c.AgentByID(ref); a != nil {
		return a
	}
	for i := range c.Agents.List {
		if strings.EqualFold(strings.TrimSpace(c.Agents.List[i].Name), ref) {
			return &c.Agents.List[i]
		}
	}
	return nil
}

// DisplayName is the agent's name, trimmed, or its id when it has none. It
// is the one way an agent is named to people and in logs.
func (a *AgentConfig) DisplayName() string {
	if name := strings.TrimSpace(a.Name); name != "" {
		return name
	}
	return a.ID
}

// AgentHasFusion reports whether the agent has the Fusion tool suite enabled.
func (c *Config) AgentHasFusion(agentID string) bool {
	for i := range c.Agents.List {
		if SameAgentID(c.Agents.List[i].ID, agentID) {
			return c.Agents.List[i].Fusion
		}
	}
	return false
}

// AgentSuiteEnabled reports whether the named all-or-nothing tool suite is
// enabled for the agent. Suites are gated as a unit by a per-agent flag rather
// than the per-tool allowlist. cogmem defaults ON; maestro, fusion and forum
// default OFF.
func (c *Config) AgentSuiteEnabled(agentID, suite string) bool {
	for i := range c.Agents.List {
		if SameAgentID(c.Agents.List[i].ID, agentID) {
			a := &c.Agents.List[i]
			switch suite {
			case "maestro":
				return a.MaestroEnabled()
			case "fusion":
				return a.Fusion
			case "forum":
				return a.Forum
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

// channelSupportsDefaultDelivery reports whether a channel can serve as an
// agent's default (async) delivery target for cron output and Integration Token
// messages. webui is excluded: its only address is a per-browser session id
// minted per connection (webui.go), so it has no durable chat that async output
// could be delivered to. Delivery targets must be durable channels (Telegram,
// Slack, Discord, …).
func channelSupportsDefaultDelivery(channel string) bool {
	return channel != "webui"
}

// ValidateBindings rejects an inconsistent binding set: more than one default
// per agent, a default on a channel with no durable delivery address (webui),
// or a default that does not resolve to a concrete chat (needs Match.Channel +
// Match.Peer{Kind,ID} or DeliverTo) and so could not receive cron output.
func (c *Config) ValidateBindings() error {
	defaults := map[string]int{}
	for i := range c.Bindings {
		b := &c.Bindings[i]
		if !b.Default {
			continue
		}
		key := b.AgentID // "" is the default agent
		if key != "" {
			key = NormalizeAgentID(key)
		}
		defaults[key]++
		if defaults[key] > 1 {
			return fmt.Errorf("agent %q has more than one default binding", b.AgentID)
		}
		if !channelSupportsDefaultDelivery(b.Match.Channel) {
			return fmt.Errorf("channel %q cannot be an agent's default channel: it has no durable delivery address", b.Match.Channel)
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
	// Models is not omitempty: the defaults template fills it, so an emptied
	// list must be written out or the next load would bring the template back.
	Models              []string `json:"models"`
	ImageModel          string   `json:"image_model,omitempty"           env:"CLAW_AGENTS_DEFAULTS_IMAGE_MODEL"`
	ImageModelFallbacks []string `json:"image_model_fallbacks,omitempty"`
	// VisionModel is the side-model used to describe images for a text-only
	// primary model (vision off): when the active model can't see images, they
	// are dispatched to this model for a one-shot text description that is then
	// injected so the active model still benefits from them. Unset = feature off
	// (images are dropped from dispatch, with a hidden-attachment note).
	// VisionModelFallbacks are tried in order when the primary vision model fails.
	VisionModel          string   `json:"vision_model,omitempty"          env:"CLAW_AGENTS_DEFAULTS_VISION_MODEL"`
	VisionModelFallbacks []string `json:"vision_model_fallbacks,omitempty"`
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
	// MaxSubagentDepth bounds sub-agent recursion: a primary turn is depth 0, its
	// spawned workers depth 1, theirs depth 2, and so on; a spawn is refused once
	// the spawning agent is already at this depth. 0 falls back to
	// DefaultMaxSubagentDepth. Applies to both agent_spawn and maestro dispatch.
	MaxSubagentDepth int `json:"max_subagent_depth,omitempty"    env:"CLAW_AGENTS_DEFAULTS_MAX_SUBAGENT_DEPTH"`
	// MaxConcurrentTurns caps how many turns run at once across all sessions;
	// further turns wait for a free slot. 0 = unlimited. Read at startup.
	MaxConcurrentTurns int `json:"max_concurrent_turns,omitempty"  env:"CLAW_AGENTS_DEFAULTS_MAX_CONCURRENT_TURNS"`
	// DailySpendAlertUSD raises one operator alert the first time the day's
	// (UTC) summed model cost reaches this amount. 0 = off.
	DailySpendAlertUSD float64 `json:"daily_spend_alert_usd,omitempty" env:"CLAW_AGENTS_DEFAULTS_DAILY_SPEND_ALERT_USD"`
	// ProgressInterval is how often (seconds) a long-running turn emits a
	// lightweight progress update so it never looks dead. 0 falls back to
	// DefaultProgressInterval; a negative value disables progress updates.
	ProgressInterval  int      `json:"progress_interval,omitempty"     env:"CLAW_AGENTS_DEFAULTS_PROGRESS_INTERVAL"`
	MaxTokens         int      `json:"max_tokens"                      env:"CLAW_AGENTS_DEFAULTS_MAX_TOKENS"`
	Temperature       *float64 `json:"temperature,omitempty"           env:"CLAW_AGENTS_DEFAULTS_TEMPERATURE"`
	MaxToolIterations int      `json:"max_tool_iterations"             env:"CLAW_AGENTS_DEFAULTS_MAX_TOOL_ITERATIONS"`
	ContextWindow     int      `json:"context_window,omitempty"        env:"CLAW_AGENTS_DEFAULTS_CONTEXT_WINDOW"`
	MaxMediaSize      int      `json:"max_media_size,omitempty"        env:"CLAW_AGENTS_DEFAULTS_MAX_MEDIA_SIZE"`
	// Compression is the default context-compaction policy for every agent
	// (overridable per agent via AgentConfig.Compression). See CompressionConfig.
	Compression *CompressionConfig `json:"compression,omitempty"`

	// Legacy flat compress_* keys — see the matching note on AgentConfig. All
	// pointers so migrateCompressionConfigs can tell "unset" from "set to 0";
	// the old plain-int form could not express "off".
	CompressMinPercent         *int     `json:"compress_min_percent,omitempty"`
	CompressNormalPercent      *int     `json:"compress_normal_percent,omitempty"`
	CompressSafetyPercent      *int     `json:"compress_safety_percent,omitempty"`
	CompressMessageThreshold   *int     `json:"compress_message_threshold,omitempty"`
	CompressRetainTokenPercent *int     `json:"compress_retain_token_percent,omitempty"`
	CompressRetainMinMessages  *int     `json:"compress_retain_min_messages,omitempty"`
	CompressCharsPerToken      *float64 `json:"compress_chars_per_token,omitempty"`
	CompressTokenSafetyMargin  *float64 `json:"compress_token_safety_margin,omitempty"`

	ArchiveMessageCount    int      `json:"archive_message_count,omitempty"         env:"CLAW_AGENTS_DEFAULTS_ARCHIVE_MESSAGE_COUNT"`
	ArchiveDays            int      `json:"archive_days,omitempty"                  env:"CLAW_AGENTS_DEFAULTS_ARCHIVE_DAYS"`
	SummaryMaxCount        int      `json:"summary_max_count,omitempty"             env:"CLAW_AGENTS_DEFAULTS_SUMMARY_MAX_COUNT"`
	SummaryRetentionDays   int      `json:"summary_retention_days,omitempty"        env:"CLAW_AGENTS_DEFAULTS_SUMMARY_RETENTION_DAYS"`
	ArchiveContentMaxBytes int      `json:"archive_content_max_bytes,omitempty"     env:"CLAW_AGENTS_DEFAULTS_ARCHIVE_CONTENT_MAX_BYTES"`
	DefaultTools           []string `json:"default_tools,omitempty"`

	// ContextEviction is the default per-turn tool-result eviction policy
	// (overridable per agent via AgentConfig.ContextEviction).
	ContextEviction *ContextEvictionConfig `json:"context_eviction,omitempty"`

	// Memory is the default cognitive-memory config applied to agents allowed
	// the cogmem tools (overridable per agent via AgentConfig.Memory).
	Memory MemoryConfig `json:"memory"`
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

// DefaultMaxSubagentDepth is the sub-agent recursion bound used when
// AgentDefaults.MaxSubagentDepth is unset (0).
const DefaultMaxSubagentDepth = 3

// GetMaxSubagentDepth returns the configured sub-agent recursion bound, or
// DefaultMaxSubagentDepth when unset (0) or negative.
func (d *AgentDefaults) GetMaxSubagentDepth() int {
	if d.MaxSubagentDepth > 0 {
		return d.MaxSubagentDepth
	}
	return DefaultMaxSubagentDepth
}

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

// DefaultModelName returns the first model in the list, or "" if unset.
func (d *AgentDefaults) DefaultModelName() string {
	if len(d.Models) == 0 {
		return ""
	}
	return d.Models[0]
}

// SetDefaultModel makes modelName the first entry in the model list,
// preserving any existing remaining entries. An empty modelName removes the
// first entry instead, so the list never holds a blank slot.
func (d *AgentDefaults) SetDefaultModel(modelName string) {
	switch {
	case modelName == "":
		if len(d.Models) > 0 {
			d.Models = d.Models[1:]
		}
	case len(d.Models) == 0:
		d.Models = []string{modelName}
	default:
		d.Models[0] = modelName
	}
}
