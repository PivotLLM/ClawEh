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
	Config               *config.Config
	AgentName            string
	GetModelInfo         func() (name, provider, apiBase string)
	ListAgentIDs         func() []string
	ListDefinitions      func() []Definition
	GetEnabledChannels   func() []string
	SwitchModel          func(value string) (oldModel string, err error)
	SwitchChannel        func(value string) error
	ClearHistory         func() error
	CompactHistory       func(ctx context.Context) error
	ResetCooldown        func()
	RetriggerLastMessage func(ctx context.Context) error
	CancelPending        func() int // drains pending queued messages; returns skip count

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
