package tools

import (
	"context"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// ToolDescriptor holds static metadata for a single tool — no deps required.
type ToolDescriptor struct {
	Name           string // tool name as returned by Tool.Name()
	Description    string // one-line description for the GUI
	Category       string // GUI grouping: "filesystem", "web", "session", etc.
	ConfigKey      string // maps to the config flag that enables/disables this tool
	DefaultEnabled bool   // include in the default agent tool allowlist
	// Suite, when non-empty, marks this as an all-or-nothing tool suite (e.g.
	// "cogmem", "maestro"): it is managed by a single per-agent toggle rather than
	// the per-tool allowlist, and the GUI renders it as one (read-only) entry
	// instead of listing every tool.
	Suite string
}

// SuiteProvider is an optional interface a tool provider implements to declare
// itself an all-or-nothing suite. Its tools are gated as a unit by the per-agent
// flag named by Suite() (resolved via Config.AgentSuiteEnabled), bypassing the
// per-tool allow/deny machinery, and are collapsed to a single GUI entry.
type SuiteProvider interface {
	Suite() string
}

// ToolProvider is implemented by each tool package. It declares what namespace
// it owns, whether it can run in the current environment, and how to build its
// tools given runtime dependencies.
type ToolProvider interface {
	Namespace() string // e.g. "files", "web", "session"
	Description() string
	Category() string
	ConfigKey() string                           // maps to config struct field name
	Available(cfg *config.Config) (bool, string) // (ok, reason if not)
	Build(deps ToolDeps) []Tool
	Describe() []ToolDescriptor
}

// ToolDeps carries everything a tool package needs at construction time.
// Fields are optional — providers check for nil/zero before using.
type ToolDeps struct {
	Cfg       *config.Config
	AgentCfg  *config.AgentConfig // nil for the default agent
	AgentID   string
	Workspace string

	// Spawn/subagent
	Provider          providers.LLMProvider
	Dispatcher        *providers.ProviderDispatcher
	Fallback          *providers.FallbackChain
	Candidates        []providers.FallbackCandidate
	SpawnAllowlist    func(callerID, targetID string) bool
	CandidateResolver func(agentID string) ([]providers.FallbackCandidate, bool)

	// Spawn holds the robust sub-agent launcher (a global.Spawner). The AgentLoop
	// builds it per agent and sets it here; toGlobalDeps forwards it to
	// global.Deps.Spawn so any tool package can launch workers by DI. Typed as any
	// to keep pkg/tools free of an import dependency on the spawner implementation.
	Spawn any

	// Session tools (closures built by AgentLoop). CompactFn returns a
	// human-readable compaction report and the resulting rendered summary,
	// alongside any error.
	CompactFn     func(ctx context.Context, sessionKey string) (report, summary string, err error)
	SessionInfoFn func(ctx context.Context, sessionKey string) (*SessionInfo, error)
	// ClearFn clears the active conversation (preserving the archive) and hands
	// the agent a fresh turn, optionally delivering message as a self-handoff.
	// nil when session_clear is not enabled for the agent.
	ClearFn func(ctx context.Context, sessionKey, message string) error

	// Shared pre-built tool instances
	MessageTool Tool // shared msg_send instance; may be nil
}
