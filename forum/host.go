// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"strings"
	"time"
)

// Outcome is the result of one Ask: how the participant's turn ended.
type Outcome string

// Outcomes of an Ask. Only OutcomeOK is a successful attempt.
const (
	OutcomeOK        Outcome = "ok"        // the agent replied; Reply.Text is the response
	OutcomeError     Outcome = "error"     // the turn failed; Reply.Text may hold the rendered error
	OutcomeCancelled Outcome = "cancelled" // the turn was cancelled before it finished
	OutcomeEmpty     Outcome = "empty"     // the agent produced no reply
	OutcomeTimeout   Outcome = "timeout"   // wait elapsed before the agent answered
)

// Successful reports whether the outcome carries a usable response.
func (o Outcome) Successful() bool { return o == OutcomeOK }

// Reply is what an Ask returns: the participant's final reply text and the
// outcome of its turn. Text is empty unless Outcome is OutcomeOK (an error
// outcome may carry the rendered error for the attempt record).
type Reply struct {
	Text    string  `json:"text"`
	Outcome Outcome `json:"outcome"`
}

// Messenger is the host's core agent-to-agent messaging: the
// core Ask function. The controller calls it for every participant turn,
// repair attempt and moderator check. The forum never whispers:
// directed messages are forum-scoped and travel inside the participant's
// next forum turn, so Whisper is not part of this interface.
type Messenger interface {
	// Ask delivers message to the agent as a normal turn in its own
	// conversation, at the maximum sub-agent depth so the turn cannot spawn
	// or ask further, and waits up to wait for the final reply. A wait that
	// elapses returns Reply{Outcome: OutcomeTimeout} with a nil error. The
	// error return is for transport failures only: the agent does not exist,
	// the host is shutting down (an error wrapping ErrShuttingDown), or ctx
	// was cancelled (the ctx error is returned). A turn the host cancels
	// because it is shutting down must be reported as ErrShuttingDown, not
	// as Reply{Outcome: OutcomeCancelled}: the forum then leaves the attempt
	// uncertain and resumes at the next start instead of recording a failed
	// attempt. Every call, whatever its result, is one of the forum's calls.
	// ctx carries the forum the ask is made for (AskInfoFromContext), so
	// the host can name the launching agent as the sender. When Ask
	// returns without the reply (the wait elapsed, ctx ended), the host
	// should stop the agent's turn, so a model call nobody waits for any
	// more is not left running.
	Ask(ctx context.Context, agentID, message string, wait time.Duration) (Reply, error)
}

// AskInfo is the forum an Ask is made for: its ID and its launcher.
type AskInfo struct {
	ForumID string
	Origin  Origin
}

type askInfoKey struct{}

// WithAskInfo returns ctx carrying info for Messenger.Ask. The controller
// sets it on every ask; a host's tests use it to make one.
func WithAskInfo(ctx context.Context, info AskInfo) context.Context {
	return context.WithValue(ctx, askInfoKey{}, info)
}

// AskInfoFromContext returns the forum a Messenger.Ask call is made for. The
// controller sets it on every Ask; ok is false for any other context.
func AskInfoFromContext(ctx context.Context) (AskInfo, bool) {
	info, ok := ctx.Value(askInfoKey{}).(AskInfo)
	return info, ok
}

// ModelInfo describes one model an agent may use. It never carries
// credentials.
type ModelInfo struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Protocol string `json:"protocol"`
	Vision   bool   `json:"vision"`
	NoTools  bool   `json:"no_tools"`
}

// FreshMode is how a fresh temporary participant keeps state between
// messages (its `mode`). The host maps it onto its registry's modes.
type FreshMode string

// Fresh participant modes.
const (
	FreshModeMemory     FreshMode = "memory"      // default: keeps its conversation, new empty cognitive memory
	FreshModeContext    FreshMode = "context"     // keeps its conversation, no memory
	FreshModeSingleShot FreshMode = "single_shot" // no memory, blank context on every message
)

// CloneSpec describes a clone participant to create (the `clone` form).
type CloneSpec struct {
	// Source is the agent to clone. It must already have passed MayTarget.
	Source string
	// Model, when set, is one of Source's models and is the model the clone
	// runs on for this forum. Empty keeps the source's default.
	Model string
	// Owner is the launching agent; the host records it as the temporary
	// agent's owner.
	Owner string
}

// FreshSpec describes a fresh temporary participant to create (the `model`
// form). The agent has no tools and no workspace prompt files.
type FreshSpec struct {
	// Model is one of the launching agent's models (validated in preflight).
	Model string
	// SystemPrompt replaces the host's neutral default when set.
	SystemPrompt string
	// Mode is the state mode; FreshModeMemory when empty.
	Mode FreshMode
	// Owner is the launching agent.
	Owner string
}

// Agents is the host's agent registry as the forum needs it.
// Temporary agents are created with the host's default idle TTL (24 h);
// the forum deletes them itself and uses the TTL only as a backstop.
type Agents interface {
	// Exists reports whether an agent with this ID is currently registered
	// (configured or temporary).
	Exists(ctx context.Context, agentID string) (bool, error)
	// MayTarget reports whether launcher may name target as an existing or
	// clone participant (the launcher's `subagents.allow_agents`).
	MayTarget(ctx context.Context, launcherID, targetID string) (bool, error)
	// Models lists the models agentID may use: the launching agent's list for
	// fresh participants, a source agent's list for a `model` override on an
	// existing or clone participant.
	Models(ctx context.Context, agentID string) ([]ModelInfo, error)
	// CreateClone creates a temporary clone of spec.Source and returns its
	// UUID. It fails if the source is gone or cannot be cloned (a human
	// agent).
	CreateClone(ctx context.Context, spec CloneSpec) (agentID string, err error)
	// CreateFresh creates a fresh temporary agent and returns its UUID.
	CreateFresh(ctx context.Context, spec FreshSpec) (agentID string, err error)
	// Delete removes a temporary agent the forum launched by launcherID
	// created. Deleting an agent that is already gone is not an error. An
	// agent still in a turn (one the forum stopped waiting for) may be
	// deleted by the host once that turn ends, returning an error wrapping
	// ErrDeletePending; the host may instead refuse it, or an agent that is
	// not a forum participant owned by launcherID (the cleanup marker lives in the launcher's workspace
	// and is not trusted); the caller retries later (the TTL is the
	// backstop).
	Delete(ctx context.Context, launcherID, agentID string) error
	// Touch refreshes the last-used time of a temporary agent the forum
	// launched by launcherID created, so a paused forum keeps its
	// participants alive past the idle TTL. The same refusal applies.
	Touch(ctx context.Context, launcherID, agentID string) error
}

// Origin identifies the launching agent and the message that launched the
// forum, so the completion notice can be delivered as a delayed reply to it
// . The host fills it from the launching tool call and the
// forum persists it in snapshot.json; fields other than AgentID are opaque
// to the forum.
type Origin struct {
	AgentID string `json:"agent_id"`
	Channel string `json:"channel,omitempty"`
	ChatID  string `json:"chat_id,omitempty"`
	Session string `json:"session,omitempty"`
}

// Chat is the chat a run was launched from, as the launching tool call
// reported it (its channel and chat ID). The service keeps it in memory
// only and never reads it from the forum's directory, so the host may
// trust it; it is the zero Chat when the launch was not seen by this
// process (a run resumed or notified after a restart) or did not come from
// a chat.
type Chat struct {
	Channel string
	ChatID  string
}

// Notifier tells the launching agent that a forum reached a terminal state.
type Notifier interface {
	// ForumFinished delivers the notice as a delayed reply to the launching
	// message: to chat when it is known, otherwise as the host decides from
	// origin (whose chat, read from the forum's directory, is untrusted).
	// It is called only after result.json is committed. An error
	// is logged by the service; the forum's state does not depend on it.
	//
	// It should hand the notice off and return rather than wait for the
	// launcher's turn. The service calls it on a goroutine of its own with
	// no lock held, so even a blocking Notifier never stalls a forum
	// operation (including a forum tool called from the turn the notice
	// starts), but Service.Close waits for it until Close's context ends.
	// ctx is cancelled when the service closes; a notice that fails then
	// is delivered at the next start.
	ForumFinished(ctx context.Context, origin Origin, chat Chat, result *Result) error
}

// Logger is the subset of the host's logger the package uses. ClawEh's
// *logger.Logger satisfies it directly.
type Logger interface {
	Debugf(format string, v ...any)
	Infof(format string, v ...any)
	Warnf(format string, v ...any)
	Errorf(format string, v ...any)
}

// SchemaViolationError is the error a compiled schema's Validate returns
// for an instance that does not satisfy the schema.
type SchemaViolationError struct {
	// Messages are human-readable, one per failing location, suitable for a
	// repair message.
	Messages []string
}

// Error joins the messages with "; ".
func (e *SchemaViolationError) Error() string {
	if len(e.Messages) == 0 {
		return "schema violation"
	}
	return "schema violation: " + strings.Join(e.Messages, "; ")
}

// Host bundles the host-provided dependencies the service needs. OnStuck
// and Cooldown may be nil; the others are required.
type Host struct {
	Messenger Messenger
	Agents    Agents
	Notifier  Notifier
	Logger    Logger
	// OnStuck, when set, is called once per run and process when a
	// forum's run stops on an error it does not recover from by itself (a
	// store write that fails during a run, a forum Recover cannot reopen):
	// the run keeps its status on disk with no live controller and continues
	// only with forum_resume or at the next start. run is the run number (0
	// when it could not be read). err is the cause, already
	// logged at Error; origin is the launcher, so the host can tell it and
	// raise an operator alert. A corrupt forum is not stuck: it ends failed
	// (EndCorrupt) and the launcher gets the normal completion notice. It
	// is called on the run's goroutine with no lock held and must not
	// block.
	OnStuck func(forumID string, run int, origin Origin, err error)
	// Cooldown, when set, reports whether every model agentID can run on
	// is in cooldown: the model that is available first and how long until
	// it is (0 when one can be used now). A turn is held back while it is
	// positive rather than sent to fail, so a cooldown does not use up the
	// turn's attempts; the hold counts against the call timeout. It must
	// not block.
	Cooldown func(agentID string) (model string, remaining time.Duration)
}
