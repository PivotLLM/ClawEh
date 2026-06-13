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
	ClearHistory   func() error
	CompactHistory func(ctx context.Context) (report string, err error)
	ResetCooldown  func()
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

	Uptime          func() time.Duration
	GetSessionStats func() (msgCount int, estTokens int, summaryChars int)

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
}

// ModelEntry is one configured candidate model for an agent, surfaced by
// /list models and /model.
type ModelEntry struct {
	Name     string
	Provider string
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
