// ClawEh - Cognitive Memory
// License: MIT

// Package consolidate implements the background "sleep cycle": it reads new
// archive messages, assembles the consolidation input, calls the configured
// model, validates the JSON response against the contract, and applies the
// resulting operations to the cogmem store in one transaction.
//
// This file defines the input/output contract (the JSON the model sees and
// returns) and its strict validator. The model only *proposes*; an invalid
// payload is rejected wholesale so a weak model can never corrupt memory.
package consolidate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
)

// Input is the JSON object sent to the consolidation model (as the user message).
type Input struct {
	Curated      Curated      `json:"curated"`
	CurrentState CurrentState `json:"current_state"`
	NewMessages  []Message    `json:"new_messages"`
}

// Curated is the verbatim, authoritative human layer (read-only to the model).
type Curated struct {
	AgentsMD   string `json:"AGENTS_md"`
	SoulMD     string `json:"SOUL_md"`
	IdentityMD string `json:"IDENTITY_md"`
	UserMD     string `json:"USER_md"`
	MemoryMD   string `json:"MEMORY_md"`
}

// CurrentState is the existing learned memory the model may update.
type CurrentState struct {
	Domains []DomainView `json:"domains"`
}

// DomainView is a compact projection of a domain for the model.
type DomainView struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Name     string            `json:"name"`
	Status   string            `json:"status"`
	Version  int64             `json:"version"`
	Summary  string            `json:"summary"`
	State    store.DomainState `json:"state"`
	Memories []MemoryView      `json:"memories"`
}

// MemoryView is a compact projection of a hook for the model.
type MemoryView struct {
	ID         string  `json:"id"`
	Type       string  `json:"type"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
}

// Message is one archive message in the batch.
type Message struct {
	Seq  int64  `json:"seq"`
	Role string `json:"role"`
	Text string `json:"text"`
}

// Output is the strict JSON the model must return.
type Output struct {
	DomainOps      []DomainOp    `json:"domain_ops"`
	MemoryOps      []MemoryOp    `json:"memory_ops"`
	ConflictLedger []LedgerEntry `json:"conflict_ledger"`
}

// DomainOp is a create/update/archive operation on a domain.
type DomainOp struct {
	Op              string             `json:"op"`
	TmpID           string             `json:"tmp_id,omitempty"`
	ID              string             `json:"id,omitempty"`
	Type            string             `json:"type,omitempty"`
	Name            string             `json:"name,omitempty"`
	Summary         string             `json:"summary,omitempty"`
	Status          string             `json:"status,omitempty"`
	Triggers        string             `json:"triggers,omitempty"` // comma-delimited tool-name substrings (auto-load)
	Reason          string             `json:"reason,omitempty"`
	ExpectedVersion *int64             `json:"expected_version,omitempty"`
	State           *store.DomainState `json:"state,omitempty"`
	Evidence        store.Evidence     `json:"evidence"`
}

// MemoryOp is an add/supersede/retire operation on a hook.
type MemoryOp struct {
	Op         string         `json:"op"`
	Domain     string         `json:"domain,omitempty"` // existing domain id or a tmp_id
	OldID      string         `json:"old_id,omitempty"`
	Type       string         `json:"type,omitempty"`
	Text       string         `json:"text,omitempty"`
	Status     string         `json:"status,omitempty"`
	Source     string         `json:"source,omitempty"`
	Confidence float64        `json:"confidence,omitempty"`
	ID         string         `json:"id,omitempty"` // for retire
	Reason     string         `json:"reason,omitempty"`
	Evidence   store.Evidence `json:"evidence"`
}

// LedgerEntry records one contradiction resolution.
type LedgerEntry struct {
	Resolved string         `json:"resolved"`
	Reason   string         `json:"reason"`
	Evidence store.Evidence `json:"evidence"`
}

var (
	validDomainTypes = map[string]bool{"project": true, "workflow": true}
	validMemoryTypes = map[string]bool{"fact": true, "preference": true, "rule": true}
	validStatuses    = map[string]bool{"active": true, "review": true}
	validSources     = map[string]bool{"user_explicit": true, "assistant_inferred": true}
)

// maxTriggersLen caps the comma-delimited tool-trigger string a domain op may set.
const maxTriggersLen = 512

// Validate enforces the contract against the input. A single violation rejects
// the whole payload (the worker then leaves the watermark unchanged and retries).
func (o Output) Validate(in Input) error {
	domainIDs := map[string]bool{}
	memoryIDs := map[string]bool{}
	for _, d := range in.CurrentState.Domains {
		domainIDs[d.ID] = true
		for _, h := range d.Memories {
			memoryIDs[h.ID] = true
		}
	}
	var minSeq, maxSeq int64
	if len(in.NewMessages) > 0 {
		minSeq = in.NewMessages[0].Seq
		maxSeq = in.NewMessages[0].Seq
		for _, m := range in.NewMessages {
			if m.Seq < minSeq {
				minSeq = m.Seq
			}
			if m.Seq > maxSeq {
				maxSeq = m.Seq
			}
		}
	}

	evOK := func(e store.Evidence) error {
		if e.SeqStart > e.SeqEnd {
			return fmt.Errorf("evidence seq_start %d > seq_end %d", e.SeqStart, e.SeqEnd)
		}
		if len(in.NewMessages) == 0 || e.SeqStart < minSeq || e.SeqEnd > maxSeq {
			return fmt.Errorf("evidence [%d,%d] outside batch [%d,%d]", e.SeqStart, e.SeqEnd, minSeq, maxSeq)
		}
		return nil
	}

	// Collect tmp ids created this payload (hooks may reference them).
	tmpIDs := map[string]bool{}
	for i, op := range o.DomainOps {
		if err := evOK(op.Evidence); err != nil {
			return fmt.Errorf("domain_ops[%d]: %w", i, err)
		}
		if len(op.Triggers) > maxTriggersLen {
			return fmt.Errorf("domain_ops[%d]: triggers too long (%d > %d)", i, len(op.Triggers), maxTriggersLen)
		}
		switch op.Op {
		case "create":
			if op.TmpID == "" || tmpIDs[op.TmpID] {
				return fmt.Errorf("domain_ops[%d]: create needs a unique tmp_id", i)
			}
			if !validDomainTypes[op.Type] {
				return fmt.Errorf("domain_ops[%d]: invalid type %q", i, op.Type)
			}
			if strings.TrimSpace(op.Name) == "" {
				return fmt.Errorf("domain_ops[%d]: create needs a name", i)
			}
			if op.Status != "" && !validStatuses[op.Status] {
				return fmt.Errorf("domain_ops[%d]: invalid status %q", i, op.Status)
			}
			tmpIDs[op.TmpID] = true
		case "update":
			if !domainIDs[op.ID] {
				return fmt.Errorf("domain_ops[%d]: update unknown domain %q", i, op.ID)
			}
			if op.ExpectedVersion == nil {
				return fmt.Errorf("domain_ops[%d]: update needs expected_version", i)
			}
		case "archive":
			if !domainIDs[op.ID] {
				return fmt.Errorf("domain_ops[%d]: archive unknown domain %q", i, op.ID)
			}
		default:
			return fmt.Errorf("domain_ops[%d]: invalid op %q", i, op.Op)
		}
	}

	for i, op := range o.MemoryOps {
		if err := evOK(op.Evidence); err != nil {
			return fmt.Errorf("memory_ops[%d]: %w", i, err)
		}
		switch op.Op {
		case "add", "supersede":
			if !domainIDs[op.Domain] && !tmpIDs[op.Domain] {
				return fmt.Errorf("memory_ops[%d]: unknown domain %q", i, op.Domain)
			}
			if !validMemoryTypes[op.Type] {
				return fmt.Errorf("memory_ops[%d]: invalid type %q", i, op.Type)
			}
			if strings.TrimSpace(op.Text) == "" {
				return fmt.Errorf("memory_ops[%d]: empty text", i)
			}
			if op.Status != "" && !validStatuses[op.Status] {
				return fmt.Errorf("memory_ops[%d]: invalid status %q", i, op.Status)
			}
			if op.Source != "" && !validSources[op.Source] {
				return fmt.Errorf("memory_ops[%d]: invalid source %q", i, op.Source)
			}
			// Inferred items must be review (rule 5).
			if op.Source == "assistant_inferred" && op.Status == "active" {
				return fmt.Errorf("memory_ops[%d]: inferred item must be status=review", i)
			}
			if containsSecret(op.Text) {
				return fmt.Errorf("memory_ops[%d]: text appears to contain a secret/credential", i)
			}
			if op.Op == "supersede" && !memoryIDs[op.OldID] {
				return fmt.Errorf("memory_ops[%d]: supersede unknown old_id %q", i, op.OldID)
			}
		case "retire":
			if !memoryIDs[op.ID] {
				return fmt.Errorf("memory_ops[%d]: retire unknown memory %q", i, op.ID)
			}
		default:
			return fmt.Errorf("memory_ops[%d]: invalid op %q", i, op.Op)
		}
	}

	for i, e := range o.ConflictLedger {
		if err := evOK(e.Evidence); err != nil {
			return fmt.Errorf("conflict_ledger[%d]: %w", i, err)
		}
	}
	return nil
}

// secretPatterns is a conservative first-pass detector. During integration this
// should be augmented with the existing MCP token scrubber (see COGMEM-TODO §4).
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bapi[_-]?key\b\s*[:=]`),
	regexp.MustCompile(`(?i)\b(password|passwd|secret|token)\b\s*[:=]\s*\S`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{16,}\b`),       // OpenAI-style
	regexp.MustCompile(`\bSST[0-9a-fA-F]{32,}\b`),       // ClawEh session tokens
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),          // AWS access key id
	regexp.MustCompile(`\b[0-9a-fA-F]{40,}\b`),          // long hex secrets
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY`), // PEM private keys
}

func containsSecret(text string) bool {
	for _, re := range secretPatterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}
