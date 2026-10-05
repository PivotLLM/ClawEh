// ClawEh
// License: MIT

package tools

import (
	"context"
	"errors"
	"time"
)

// OutcomeTimeout is the AgentReply outcome of an ask the target did not answer
// in time. The other outcomes are the bus turn outcomes (bus.OutcomeOK,
// bus.OutcomeError, bus.OutcomeCancelled, bus.OutcomeEmpty).
const OutcomeTimeout = "timeout"

// Errors returned by Messenger.
var (
	// ErrNoSuchAgent: the addressed agent does not exist.
	ErrNoSuchAgent = errors.New("there is no such agent")
	// ErrMaxDepth: the asking turn is already at agents.defaults.max_subagent_depth.
	ErrMaxDepth = errors.New("maximum sub-agent depth reached")
	// ErrAskLoop: the target is itself waiting for a reply in this exchange, so
	// asking it would wait until the timeout.
	ErrAskLoop = errors.New("the agent is waiting for a reply in this exchange")
)

// AgentReply is the answer to an ask.
type AgentReply struct {
	// Text is the target's final reply; for OutcomeTimeout, a one-line note
	// naming the agent and the wait.
	Text string
	// Outcome is ok, error, cancelled, empty or timeout.
	Outcome string
}

// Messenger sends one agent's message to another. from names the sender as
// the target sees it (an agent's or a person's display name).
type Messenger interface {
	// Ask gives agentID a normal turn in its one conversation with message,
	// headed with the sender's name, and returns its final reply. It waits at
	// most wait, or the target's request_timeout when that is shorter; the
	// target's turn may run on after a timeout, and its late reply is
	// discarded. Each ask runs one sub-agent level deeper than ctx.
	Ask(ctx context.Context, from, agentID, message string, wait time.Duration) (AgentReply, error)
	// Whisper holds message for agentID and adds it, marked private, to the
	// start of the next message the agent receives. It never starts a turn.
	Whisper(ctx context.Context, from, agentID, message string) error
}
