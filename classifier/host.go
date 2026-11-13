// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Scope is one agent's classifier folder: the agent (its configured id) and
// <workspace>/classifiers as an absolute path.
type Scope struct {
	AgentID       string
	BaseDirectory string
	// Clone is the short id of the clone of AgentID whose tool call this
	// is, "" otherwise. It is recorded in the audit rows the call writes.
	Clone string
}

// ToolRunner runs the tools a classifier names as the owning agent.
type ToolRunner interface {
	// Has reports whether agentID has tool and it may run unattended
	// (registered, not session-scoped); the error names the tool and the
	// agent.
	Has(agentID, tool string) error
	// Call runs tool as agentID through its own registry. A tool the agent
	// does not have, or that may not run unattended, is an error matching
	// ErrToolUnavailable whose text names the tool; any other failure (the
	// tool's own error, no answer in time) is an error carrying the reason
	// without the tool's name, which the caller adds with the tool's role.
	// A result the host marked as untrusted external content is returned
	// unwrapped: the classifier reads it as data.
	Call(ctx context.Context, agentID, tool string, args map[string]any) (result string, err error)
}

// ErrToolUnavailable matches a ToolRunner.Call error for a tool the agent
// does not have or that a classifier may not use (ToolUnavailable).
var ErrToolUnavailable = errors.New("tool not available")

// unavailableError is ToolUnavailable's error.
type unavailableError struct{ text string }

func (e unavailableError) Error() string        { return e.text }
func (e unavailableError) Is(target error) bool { return target == ErrToolUnavailable }

// ToolUnavailable is a ToolRunner's error for a tool the agent cannot use
// from a classifier: text is the sentence naming the tool, and the error
// matches ErrToolUnavailable.
func ToolUnavailable(text string) error { return unavailableError{text: text} }

// classifierKey is the context key of the classifier a call is made for.
type classifierKey struct{}

// withClassifier marks ctx as a call made for classifier id.
func withClassifier(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, classifierKey{}, id)
}

// ClassifierID returns the id of the classifier a tool or model call is
// made for, "" when ctx carries none. The host uses it to name the
// classifier in its log lines and in the folder of an isolated CLI call.
func ClassifierID(ctx context.Context) string {
	if id, ok := ctx.Value(classifierKey{}).(string); ok {
		return id
	}
	return ""
}

// ModelCaller asks agentID's classifier model one question, with no tools.
type ModelCaller interface {
	// Complete sends one request and returns the reply and the model name.
	// It returns an error wrapping ErrNoModel when the agent has no usable
	// classifier model, and a *CooldownError when the model is in cooldown.
	Complete(ctx context.Context, agentID, system, user string) (reply, model string, err error)
}

// NamedText is one Markdown file of an instructions folder: its file name
// and its content.
type NamedText struct {
	Name string
	Text string
}

// InstructionsReader reads a classifier's Markdown instructions as the
// owning agent, with that agent's own file-read permission: only what its
// file tools may read, and only inside its files/ area (a link leading out
// of it is refused). path is workspace-relative (ValidInstructionsPath).
// A missing file or folder is an error wrapping fs.ErrNotExist; every
// other error is a short reason ("it is a folder").
type InstructionsReader interface {
	// ReadInstructionsFile returns the content of the regular file at path,
	// refused when it has more than MaxInstructionsFileBytes bytes.
	ReadInstructionsFile(agentID, path string) (string, error)
	// ReadInstructionsDir returns every regular *.md file directly in the
	// folder at path (not recursive, hidden names skipped, a name ending in
	// .md in any case; other entries are skipped), in any order; refused
	// when one has more than MaxInstructionsFileBytes bytes or all together
	// more than MaxInstructionsDirBytes.
	ReadInstructionsDir(agentID, path string) ([]NamedText, error)
}

// Size caps of the Markdown instructions a classifier reads, in bytes.
const (
	MaxInstructionsFileBytes = 256 << 10
	MaxInstructionsDirBytes  = 1 << 20
)

// Notifier tells the owning agent and its person about runs, and the
// operator about what only the operator can fix. The host names the agent;
// text names the classifier. Errors are logged only.
type Notifier interface {
	// Summary posts a run's summary to the agent's default chat.
	Summary(ctx context.Context, agentID, text string) error
	// RunFailed delivers the notice of a failed run to the agent itself, as
	// a message in its main conversation, so it can fix the classifier.
	RunFailed(ctx context.Context, agentID, text string) error
	// StateFailed raises the operator alert for an agent whose classifier
	// state (path, its state.db) cannot be opened: its classifiers do not
	// run until the operator fixes it.
	StateFailed(ctx context.Context, agentID, path string, err error)
}

// Audit event kinds.
const (
	AuditWrite          = "classifier_write"
	AuditClassification = "classification"
)

// AuditEvent is one audit row: a definition written (Kind AuditWrite, Op
// create, update or delete, Details holding the definition or the patch) or
// an item handled (Kind AuditClassification).
type AuditEvent struct {
	Kind       string
	Agent      string
	Classifier string
	ItemID     string
	Category   string
	Action     string // the action tool, "" when none ran
	Outcome    string
	Clone      string // the short id of the clone that made the change, "" for the agent itself
	Details    map[string]any
}

// Auditor records audit events.
type Auditor interface {
	Record(ev AuditEvent)
}

// Logger is the subset of the host's logger the package uses. ClawEh's
// *logger.Logger satisfies it directly.
type Logger interface {
	Debugf(format string, v ...any)
	Infof(format string, v ...any)
	Warnf(format string, v ...any)
	Errorf(format string, v ...any)
}

// Clock is the time source of the scheduler and the state's timestamps. Its
// methods are the time package's, so a fake clock satisfies it.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Host bundles what the service needs from ClawEh. Tools, Model and Logger
// are required; the others may be nil.
type Host struct {
	Tools ToolRunner
	Model ModelCaller
	// Instructions reads classify.instructions_file and instructions_dir.
	// nil: a classifier naming either fails its runs.
	Instructions InstructionsReader
	Notifier     Notifier // nil: summaries are not posted
	Auditor      Auditor  // nil: nothing is audited
	Logger       Logger
	Clock        Clock // nil: the system clock
	// Modes gives the exact folder and file modes of what is created under
	// base (a scope's BaseDirectory). nil or zero means 0700/0600.
	Modes func(base string) (dir, file os.FileMode)
	// Ceilings returns the install's maximums; read at every validation and
	// run. nil: no maximums.
	Ceilings func() Ceilings
	// Scopes returns every configured agent whose classifiers switch is on.
	// nil: none.
	Scopes func() []Scope
	// Redact removes secrets (API keys, credentials) from an error's text
	// before it is stored, logged, sent in a notice or returned by a tool.
	// nil: texts are kept as they are.
	Redact func(string) string
}

func (h *Host) clock() Clock {
	if h.Clock == nil {
		return systemClock{}
	}
	return h.Clock
}

func (h *Host) ceilings() Ceilings {
	if h.Ceilings == nil {
		return Ceilings{}
	}
	return h.Ceilings()
}

func (h *Host) modes(base string) (dir, file os.FileMode) {
	if h.Modes != nil {
		dir, file = h.Modes(base)
	}
	if dir == 0 {
		dir = 0o700
	}
	if file == 0 {
		file = 0o600
	}
	return dir, file
}

func (h *Host) redact(s string) string {
	if h.Redact == nil {
		return s
	}
	return h.Redact(s)
}

func (h *Host) audit(ev AuditEvent) {
	if h.Auditor != nil {
		h.Auditor.Record(ev)
	}
}

// ErrNoModel is the model caller's refusal when the agent has no usable
// classifier model (none set, or not one of the agent's models).
var ErrNoModel = errors.New("no usable classifier model")

// CooldownError is the model caller's refusal while the classifier model is
// in cooldown.
type CooldownError struct {
	Model     string
	Remaining time.Duration
}

func (e *CooldownError) Error() string {
	return fmt.Sprintf("model %s is in cooldown for %s", e.Model, e.Remaining.Round(time.Second))
}

// ErrNotFound: no classifier with that id is owned by the scope's agent.
var ErrNotFound = errors.New("classifier not found")

// ErrExists: a classifier with that id already exists in the folder.
var ErrExists = errors.New("classifier already exists")

// ErrItemNotFound: the source does not list the item.
var ErrItemNotFound = errors.New("item not found")

// ErrNoItems: the source lists no item.
var ErrNoItems = errors.New("the source lists no items")

// ErrClosed: the service is closed.
var ErrClosed = errors.New("classifier service closed")

// ErrStateUnavailable: a folder's state.db cannot be opened. Only the
// operator can fix it.
var ErrStateUnavailable = errors.New("the classifier's state could not be opened")

// ValidationError is a refused definition: one plain sentence per issue.
type ValidationError struct {
	Issues []string
}

func (e *ValidationError) Error() string { return strings.Join(e.Issues, "\n") }

// Refusal marks the error as an expected refusal (tools.IsExpectedRefusal).
func (e *ValidationError) Refusal() bool { return true }
