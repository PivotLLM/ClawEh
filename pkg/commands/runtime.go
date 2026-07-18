package commands

import (
	"context"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// Runtime provides runtime dependencies to command handlers. It is constructed
// per-request by the agent loop so that per-request state (like session scope)
// can coexist with long-lived callbacks (like GetModelInfo).
type Runtime struct {
	Config             *config.Config
	AgentName          string
	GetModelInfo       func() (name, provider, protocol, apiBase string)
	ListAgentIDs       func() []string
	ListDefinitions    func() []Definition
	GetEnabledChannels func() []string
	SwitchChannel      func(value string) error

	// GetAgentModels returns the agent's configured candidate models (numbered
	// from 0 in order) and the index of the session's currently active model.
	GetAgentModels func() (entries []ModelEntry, active int)
	// SetActiveModel sets the session's active model to the 0-based index and
	// returns the selected model's name. Returns an error for an out-of-range
	// index or when the agent has no selectable models.
	SetActiveModel func(idx int) (name string, err error)
	// GetExposeReasoning / SetExposeReasoning read and toggle whether this session
	// delivers the model's reasoning to the user (/reasoning). Nil when the host
	// does not support it.
	GetExposeReasoning func() bool
	SetExposeReasoning func(on bool)
	// GetShowToolActivity / SetShowToolActivity read and toggle whether this session
	// posts a one-line breadcrumb for each tool call (/tools). Nil when the host
	// does not support it.
	GetShowToolActivity func() bool
	SetShowToolActivity func(on bool)
	ClearHistory       func() error
	CompactHistory     func(ctx context.Context) (report string, err error)
	ResetCooldown      func()
	// ClearCooldown clears the cooldown for a single provider/model and
	// returns true when an entry existed. Used by `/cooldowns clear <p/m>`
	// to surface a "no cooldown found" message when the entry was already
	// clear (informational, not an error).
	ClearCooldown        func(provider, model string) bool
	RetriggerLastMessage func(ctx context.Context) error
	CancelPending        func() int // drains pending queued messages; returns skip count

	// ListCooldowns returns the process-wide snapshot of models that are
	// currently in cooldown or billing-disabled. Returns nil on no
	// implementation; the renderer must handle that as "feature unavailable".
	ListCooldowns func() []CooldownEntry

	// ListTokenQuota returns the per-token rate-limit status for THIS agent's
	// named message-API tokens; ResetTokenQuota clears blocks (empty name = all)
	// and returns the count cleared. Nil when the host has no message-token
	// store; the /quota renderer treats that as "feature unavailable".
	ListTokenQuota  func() []TokenQuotaEntry
	ResetTokenQuota func(name string) int

	Uptime          func() time.Duration
	GetSessionStats func() (msgCount int, estTokens int, summaryChars int)

	// GetContextWindow returns the context-window size (in tokens) that governs
	// compaction and eviction for this session — i.e. the budget against which
	// GetSessionStats' estimated tokens are measured. Returns 0 when unknown.
	GetContextWindow func() int

	// GetSessionChannels returns the channels reachable by the agent that owns
	// this session — NOT every channel the daemon has configured. Used by
	// /status to avoid leaking other agents' channel surface area.
	// A nil return or empty slice means "no per-agent binding registered"; the
	// caller may fall back to req.Channel.
	GetSessionChannels func() []string

	// GetArchiveStats returns the archive message count and the first/last
	// created_at timestamps for THIS session. Returns (0, zero, zero) when no
	// archive exists. Implementations must be cheap (single SQL aggregate or
	// equivalent) and must not load message bodies.
	GetArchiveStats func() (count int, first, last time.Time)

	// GetMemoryStatus returns a human-readable cognitive-memory summary for THIS
	// session (active domain/memory counts, pending-review count, and the last
	// consolidation run). Returns "" when the agent has no cognitive memory.
	GetMemoryStatus func() string
}

// ModelEntry is one configured candidate model for an agent, surfaced by
// /list models and /model.
type ModelEntry struct {
	Name     string
	Provider string
}

// TokenQuotaEntry is one named message-API token's rate-limit status, surfaced
// by /quota. Decoupled from pkg/msgtoken so the commands package does not import
// it; the agent loop maps msgtoken.TokenQuota onto this shape.
type TokenQuotaEntry struct {
	Name           string
	RatePerMin     int
	BlockMinutes   int
	HitsInWindow   int
	Blocked        bool
	BlockRemaining time.Duration
}

// CooldownEntry is a single row of the process-wide cooldown snapshot
// surfaced in /status and /cooldowns. Decoupled from the providers package
// so the commands package doesn't import providers.
type CooldownEntry struct {
	Provider string
	Model    string
	Reason   string
	Since    time.Time
	Until    time.Time
}
