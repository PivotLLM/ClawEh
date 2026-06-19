// ClawEh - Cognitive Memory
// License: MIT

// Package store is the per-session persistence layer for cognitive memory.
// It owns the .cogmem.db SQLite database (modernc.org/sqlite, pure Go, no CGO)
// and holds the storage types shared by the composer, the consolidation worker,
// and the cogmem MCP tools. It is the leaf package: nothing in cogmem imports
// its importers, so there are no cycles.
package store

import "time"

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

// Origin records which actor created a memory, independent of Source (which
// records provenance). Surfaced to the user and into the prompt so the
// assistant knows where a memory came from.
type Origin string

const (
	OriginChat          Origin = "chat"          // the agent wrote it during a conversation (cogmem tools)
	OriginConsolidation Origin = "consolidation" // the background sleep-cycle worker wrote it
	OriginUser          Origin = "user"          // a human created it directly (WebUI / import)
)

// normalizeOrigin returns a valid Origin, defaulting unknown/empty to OriginChat.
func normalizeOrigin(o Origin) Origin {
	switch o {
	case OriginChat, OriginConsolidation, OriginUser:
		return o
	default:
		return OriginChat
	}
}

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
	// StickyPriority is stored in the legacy "type" column (TEXT, parsed as int):
	// 0 / non-numeric = not sticky; > 0 = sticky (injected into every prompt). The
	// magnitude is reserved as a future sort key. See Sticky.
	StickyPriority int
	Name          string
	Status        Status
	Version       int64
	Summary       string
	State         DomainState
	SchemaName    string
	SchemaVersion int
	LastActiveAt  int64  // unix seconds; recency/last-used signal (0 = never recorded)
	Triggers      string // comma-delimited tool-name substrings; activates this domain when a matching tool is used (see TriggerTokens)
	KeywordTriggers string // comma-delimited phrases; activates this domain when one appears (whole-phrase, word-boundary) in the message text (see KeywordPhrases)
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ArchivedAt    *time.Time
	Memories      []Memory // populated by GetDomain / ListMemories; nil otherwise
}

// Sticky reports whether this domain is injected into every prompt.
func (d Domain) Sticky() bool { return d.StickyPriority > 0 }

// LastActive returns when the domain was last active (created, written, loaded,
// or read) and false if it was never recorded.
func (d Domain) LastActive() (time.Time, bool) {
	if d.LastActiveAt <= 0 {
		return time.Time{}, false
	}
	return time.Unix(d.LastActiveAt, 0), true
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
	Origin             Origin
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
