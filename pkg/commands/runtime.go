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
	GetModelInfo       func() (name, provider, apiBase string)
	ListAgentIDs       func() []string
	ListDefinitions    func() []Definition
	GetEnabledChannels func() []string
	SwitchModel        func(value string) (oldModel string, err error)
	SwitchChannel      func(value string) error
	ClearHistory         func() error
	CompactHistory       func(ctx context.Context) error
	ResetCooldown        func()
	RetriggerLastMessage func(ctx context.Context) error
	CancelPending        func() int // drains pending queued messages; returns skip count

	Uptime          func() time.Duration
	GetSessionStats func() (msgCount int, estTokens int, summaryChars int)
}
