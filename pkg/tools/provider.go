package tools

import (
	"context"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// ToolProvider is implemented by each tool package. It declares what namespace
// it owns, whether it can run in the current environment, and how to build its
// tools given runtime dependencies.
type ToolProvider interface {
	Namespace() string                            // e.g. "files", "web", "session"
	Description() string
	Category() string
	ConfigKey() string                            // maps to config struct field name
	Available(cfg *config.Config) (bool, string) // (ok, reason if not)
	Build(deps ToolDeps) []Tool
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

	// Session tools (closures built by AgentLoop)
	CompactFn     func(ctx context.Context, sessionKey string) error
	SessionInfoFn func(ctx context.Context, sessionKey string) (*SessionInfo, error)

	// Shared pre-built tool instances
	MessageTool Tool // shared msg_send instance; may be nil
}
