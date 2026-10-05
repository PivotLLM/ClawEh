// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Outcome is the result of one Ask: how the participant's turn ended.
type Outcome string

// Outcomes of an Ask. Only OutcomeOK is a successful attempt (§2.3).
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

// Messenger is the host's core agent-to-agent messaging (spec §12.2): the
// core Ask function. The controller calls it for every participant turn,
// repair attempt and moderator check (§2.3). The forum never whispers:
// directed messages are forum-scoped and travel inside the participant's
// next forum turn (§6), so Whisper is not part of this interface.
type Messenger interface {
	// Ask delivers message to the agent as a normal turn in its own
	// conversation, at the maximum sub-agent depth so the turn cannot spawn
	// or ask further, and waits up to wait for the final reply. A wait that
	// elapses returns Reply{Outcome: OutcomeTimeout} with a nil error. The
	// error return is for transport failures only: the agent does not exist,
	// the host is shutting down, or ctx was cancelled (the ctx error is
	// returned). Every call, whatever its result, is one of the forum's calls.
	Ask(ctx context.Context, agentID, message string, wait time.Duration) (Reply, error)
}

// ModelInfo describes one model an agent may use (§2.4). It never carries
// credentials.
type ModelInfo struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Protocol string `json:"protocol"`
	Vision   bool   `json:"vision"`
	NoTools  bool   `json:"no_tools"`
}

// FreshMode is how a fresh temporary participant keeps state between
// messages (§3.1 `mode`). The host maps it onto its registry's modes.
type FreshMode string

// Fresh participant modes.
const (
	FreshModeMemory     FreshMode = "memory"      // default: keeps its conversation, new empty cognitive memory
	FreshModeContext    FreshMode = "context"     // keeps its conversation, no memory
	FreshModeSingleShot FreshMode = "single_shot" // no memory, blank context on every message
)

// CloneSpec describes a clone participant to create (§3.1 clone form).
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

// FreshSpec describes a fresh temporary participant to create (§3.1 fresh
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

// Agents is the host's agent registry as the forum needs it (§2.1).
// Temporary agents are created with the host's default idle TTL (24 h, §9);
// the forum deletes them itself and uses the TTL only as a backstop.
type Agents interface {
	// Exists reports whether an agent with this ID is currently registered
	// (configured or temporary).
	Exists(ctx context.Context, agentID string) (bool, error)
	// MayTarget reports whether launcher may name target as an existing or
	// clone participant (the launcher's `subagents.allow_agents`, §3.1).
	MayTarget(ctx context.Context, launcherID, targetID string) (bool, error)
	// Models lists the models agentID may use: the launching agent's list for
	// fresh participants, a source agent's list for a `model` override on an
	// existing or clone participant (§2.4).
	Models(ctx context.Context, agentID string) ([]ModelInfo, error)
	// CreateClone creates a temporary clone of spec.Source and returns its
	// UUID. It fails if the source is gone or cannot be cloned (a human
	// agent, §12.1).
	CreateClone(ctx context.Context, spec CloneSpec) (agentID string, err error)
	// CreateFresh creates a fresh temporary agent and returns its UUID.
	CreateFresh(ctx context.Context, spec FreshSpec) (agentID string, err error)
	// Delete removes a temporary agent the forum created. Deleting an agent
	// that is already gone is not an error. The host may refuse while the
	// agent is mid-turn; the caller retries later (the TTL is the backstop).
	Delete(ctx context.Context, agentID string) error
	// Touch refreshes a temporary agent's last-used time so a paused forum
	// keeps its participants alive past the idle TTL (§9).
	Touch(ctx context.Context, agentID string) error
}

// Origin identifies the launching agent and the message that launched the
// forum, so the completion notice can be delivered as a delayed reply to it
// (§9 Completion). The host fills it from the launching tool call and the
// forum persists it in snapshot.json; fields other than AgentID are opaque
// to the forum.
type Origin struct {
	AgentID string `json:"agent_id"`
	Channel string `json:"channel,omitempty"`
	ChatID  string `json:"chat_id,omitempty"`
	Session string `json:"session,omitempty"`
}

// Notifier tells the launching agent that a forum reached a terminal state.
type Notifier interface {
	// ForumFinished delivers the notice as a delayed reply to the launching
	// message. It is called only after result.json is committed. An error
	// is logged by the service; the forum's state does not depend on it.
	ForumFinished(ctx context.Context, origin Origin, result *Result) error
}

// Logger is the subset of the host's logger the package uses. ClawEh's
// *logger.Logger satisfies it directly.
type Logger interface {
	Debugf(format string, v ...any)
	Infof(format string, v ...any)
	Warnf(format string, v ...any)
	Errorf(format string, v ...any)
}

// SchemaValidator compiles JSON Schemas (Draft 2020-12, internal references
// only, §3). No JSON Schema library is a direct dependency of ClawEh, so the
// host supplies one; a nil SchemaValidator makes any configuration that
// names a schema fail preflight with ErrSchemasUnavailable.
type SchemaValidator interface {
	// Compile parses and compiles one schema document. It fails on a schema
	// that is not valid Draft 2020-12 or that references anything outside
	// itself.
	Compile(schema json.RawMessage) (CompiledSchema, error)
}

// CompiledSchema validates JSON instances against one compiled schema.
type CompiledSchema interface {
	// Validate checks one JSON document. A violation is returned as a
	// *SchemaViolationError listing every failing location, so the
	// controller can hand the list to the participant for repair; any other
	// error is a validator failure.
	Validate(instance []byte) error
}

// SchemaViolationError is the error CompiledSchema.Validate returns for an
// instance that does not satisfy the schema.
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

// Host bundles the host-provided dependencies the service needs. Schemas
// may be nil (see SchemaValidator); the others are required.
type Host struct {
	Messenger Messenger
	Agents    Agents
	Notifier  Notifier
	Logger    Logger
	Schemas   SchemaValidator
}
