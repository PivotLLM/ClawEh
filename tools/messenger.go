// ClawEh
// License: MIT

package tools

import (
	"context"
	"errors"
	"strconv"
	"time"
	"unicode/utf8"
)

// MaxAgentMessageChars is the most characters (Unicode code points) one
// message to another agent may hold, from the agent_message tool and the
// /ask and /whisper commands. It keeps one agent from flooding another's
// conversation; a longer text belongs in a file the other agent can read.
// The core Ask and Whisper do not enforce it: the forum composes longer
// messages itself.
const MaxAgentMessageChars = 8000

// AgentMessageTooLong reports whether message exceeds MaxAgentMessageChars.
func AgentMessageTooLong(message string) bool {
	return utf8.RuneCountInString(message) > MaxAgentMessageChars
}

// AgentMessageLimitText is the refusal for a message over
// MaxAgentMessageChars: "Messages to other agents are limited to 8,000
// characters."
func AgentMessageLimitText() string {
	return "Messages to other agents are limited to " + groupThousands(MaxAgentMessageChars) + " characters."
}

// groupThousands renders n with a comma between groups of three digits.
func groupThousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// OutcomeTimeout is the AgentReply outcome of an ask the target did not answer
// in time. The other outcomes are the bus turn outcomes (bus.OutcomeOK,
// bus.OutcomeError, bus.OutcomeCancelled, bus.OutcomeEmpty).
const OutcomeTimeout = "timeout"

// OutcomePersonCancelled is the AgentReply outcome of an ask to a human
// agent whose person cancelled it (/cancel in their chat). Text says so,
// naming the agent.
const OutcomePersonCancelled = "person_cancelled"

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
	// Outcome is ok, error, cancelled, empty, timeout or person_cancelled.
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
	// fromID is the sending agent's id ("" when the sender is not an
	// agent); from is the name the recipient sees.
	Whisper(ctx context.Context, fromID, from, agentID, message string) error
}
