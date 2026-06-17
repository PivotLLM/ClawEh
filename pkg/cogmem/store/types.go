// ClawEh - Cognitive Memory
// License: MIT

// Package store is the per-session persistence layer for cognitive memory.
// It owns the .cogmem.db SQLite database (modernc.org/sqlite, pure Go, no CGO)
// and holds the storage types shared by the composer, the consolidation worker,
// and the cogmem MCP tools. It is the leaf package: nothing in cogmem imports
// its importers, so there are no cycles.
package store

import "time"

// DomainType classifies a memory domain. General is the single mandatory,
// always-on domain (injected every turn); the rest are routed (loaded on request
// or relevance).
type DomainType string

const (
	// DomainGeneral is the one mandatory always-on domain, seeded into every
	// session's .cogmem.db and injected into the prompt each turn. It is the home
	// for global rules, preferences, and standing facts the agent records.
	DomainGeneral  DomainType = "general"
	DomainProject  DomainType = "project"
	DomainWorkflow DomainType = "workflow"
)

// AlwaysOn reports whether a domain of this type is injected into every prompt.
func (t DomainType) AlwaysOn() bool {
	return t == DomainGeneral
}

// Status is the lifecycle state of a domain or memory.
type Status string

const (
	StatusActive   Status = "active"   // used in prompts
	StatusReview   Status = "review"   // unconfirmed; pending digest only
	StatusArchived Status = "archived" // domains
	StatusRetired  Status = "retired"  // memories
	StatusRejected Status = "rejected" // memories
)

// MemoryType classifies a memory. The set is deliberately small: what a memory
// is (fact), how the user wants things done (preference), or a hard directive
// (rule). Volatile project status lives on the domain's state, not here.
type MemoryType string

const (
	TypeFact       MemoryType = "fact"
	TypePreference MemoryType = "preference"
	TypeRule       MemoryType = "rule"
)

// Source records how a memory item came to exist.
type Source string

const (
	SourceUserExplicit      Source = "user_explicit"
	SourceAssistantInferred Source = "assistant_inferred"
	SourceToolWrite         Source = "tool_write"
	SourceMigration         Source = "migration"
)

// DomainState is the structured JSON payload stored in domains.state_json.
type DomainState struct {
	Blockers    []string       `json:"blockers,omitempty"`
	NextActions []string       `json:"next_actions,omitempty"`
	Constraints []string       `json:"constraints,omitempty"`
	Fields      map[string]any `json:"fields,omitempty"`
}

// Domain is one coherent body of learned knowledge in a session.
type Domain struct {
	ID            string
	AgentID       string
	SessionKey    string
	Type          DomainType
	Name          string
	Status        Status
	Version       int64
	Summary       string
	State         DomainState
	SchemaName    string
	SchemaVersion int
	LastActiveAt  int64  // unix-nanos ordering key for recency (0 = never), not displayed
	Triggers      string // comma-delimited tool-name substrings; activates this domain when a matching tool is used (see TriggerTokens)
	KeywordTriggers string // comma-delimited phrases; activates this domain when one appears (whole-phrase, word-boundary) in the message text (see KeywordPhrases)
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ArchivedAt    *time.Time
	Memories      []Memory // populated by GetDomain / ListMemories; nil otherwise
}

// Memory is a short, addressable unit of learned memory inside a domain.
type Memory struct {
	ID                 string
	DomainID           string
	Type               MemoryType
	Text               string
	Status             Status
	Confidence         float64
	Priority           int
	Source             Source
	SourceSession      *string
	SourceSeqStart     *int64
	SourceSeqEnd       *int64
	SupersedesMemoryID *string
	RetireReason       *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Evidence references the archive seq range that justifies a memory change.
type Evidence struct {
	SeqStart int64 `json:"seq_start"`
	SeqEnd   int64 `json:"seq_end"`
}

// Event is one row of the append-only audit ledger.
type Event struct {
	ID         string
	Type       string // create, update, retire, merge, reject, conflict_resolved, gap
	DomainID   string
	MemoryID   string
	OldJSON    string
	NewJSON    string
	Reason     string
	Evidence   string // marshaled evidence/json
	Actor      string // sleep_cycle, mcp_tool, migration, operator
	Model      string
	PromptHash string
}

// Run is one consolidation-run debug record (for comparing models).
type Run struct {
	ID           string
	Trigger      string // message, idle, nightly, manual
	Model        string
	SeqStart     int64
	SeqEnd       int64
	InputTokens  int
	OutputTokens int
	Status       string // ok, invalid_json, aborted, error
	OpsApplied   int
	Error        string
	PromptHash   string
	StartedAt    time.Time
	FinishedAt   *time.Time
}
