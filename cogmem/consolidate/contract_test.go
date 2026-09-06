// ClawEh - Cognitive Memory
// License: MIT

package consolidate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/cogmem/store"
)

func sampleInput() Input {
	return Input{
		CurrentState: CurrentState{Domains: []DomainView{{
			ID: "d4", Name: "Layout", Version: 2,
			Memories: []MemoryView{{ID: "h9", Type: "rule", Text: "Never use the color blue.", Confidence: 0.9}},
		}}},
		NewMessages: []Message{{Seq: 512, Role: "user", Text: "Actually, use blue for the layout."}},
	}
}

func TestValidateHappyPath(t *testing.T) {
	in := sampleInput()
	out := Output{
		MemoryOps: []MemoryOp{{
			Op: "supersede", OldID: "h9", Domain: "d4", Type: "rule",
			Text: "Use blue for the layout.", Confidence: 0.95, Evidence: ev(512, 512),
		}},
		ConflictLedger: []LedgerEntry{{Resolved: "x", Reason: "y", Evidence: ev(512, 512)}},
	}
	if err := out.Validate(in); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
}

func TestValidateRejections(t *testing.T) {
	in := sampleInput()
	cases := map[string]Output{
		"evidence out of range": {MemoryOps: []MemoryOp{{Op: "add", Domain: "d4", Type: "fact", Text: "ok", Evidence: ev(999, 999)}}},
		"unknown domain":        {MemoryOps: []MemoryOp{{Op: "add", Domain: "dX", Type: "fact", Text: "ok", Evidence: ev(512, 512)}}},
		"unknown retire id":     {MemoryOps: []MemoryOp{{Op: "retire", ID: "hZ", Reason: "x", Evidence: ev(512, 512)}}},
		"invalid kind":          {MemoryOps: []MemoryOp{{Op: "add", Domain: "d4", Type: "bogus", Text: "ok", Evidence: ev(512, 512)}}},
		"create no tmp_id":      {DomainOps: []DomainOp{{Op: "create", Name: "X", Evidence: ev(512, 512)}}},
		// Type is REQUIRED, not merely valid-if-present. The old contract
		// checked each field only when it was non-empty, so an op that named no
		// type and no status and no source passed every guard and was then
		// filled in with defaults — assistant_inferred + active, the one
		// combination the rules forbade.
		"missing type": {MemoryOps: []MemoryOp{{Op: "add", Domain: "d4", Text: "ok", Evidence: ev(512, 512)}}},
		"empty text":   {MemoryOps: []MemoryOp{{Op: "add", Domain: "d4", Type: "fact", Evidence: ev(512, 512)}}},
		// review was a memory status and never a domain one; the domain
		// lifecycle is active/archived.
		"domain status review": {DomainOps: []DomainOp{{
			Op: "create", TmpID: "t1", Name: "X", Status: "review", Evidence: ev(512, 512),
		}}},
	}
	for name, out := range cases {
		if err := out.Validate(in); err == nil {
			t.Errorf("%s: expected rejection, got nil", name)
		}
	}
}

// The two types added with the redesign must be accepted: event (never loaded
// into the prompt) and operational (the assistant's own housekeeping).
func TestValidateAcceptsEventAndOperational(t *testing.T) {
	in := sampleInput()
	for _, typ := range []string{"fact", "preference", "rule", "event", "operational"} {
		out := Output{MemoryOps: []MemoryOp{{
			Op: "add", Domain: "d4", Type: typ, Text: "ok",
			Confidence: 0.9, Evidence: ev(512, 512),
		}}}
		if err := out.Validate(in); err != nil {
			t.Errorf("type %q rejected: %v", typ, err)
		}
	}
}

func TestValidateTmpIDReference(t *testing.T) {
	in := sampleInput()
	out := Output{
		DomainOps: []DomainOp{{Op: "create", TmpID: "t1", Name: "New", Evidence: ev(512, 512)}},
		MemoryOps: []MemoryOp{{Op: "add", Domain: "t1", Type: "fact", Text: "a fact", Evidence: ev(512, 512)}},
	}
	if err := out.Validate(in); err != nil {
		t.Fatalf("tmp_id reference rejected: %v", err)
	}
}

// TestValidateUpdateIsPatch confirms a domain update no longer requires a version
// (the patch model dropped expected_version) and accepts an optional sticky flag.
func TestValidateUpdateIsPatch(t *testing.T) {
	in := sampleInput()
	yes := true
	out := Output{
		DomainOps: []DomainOp{{Op: "update", ID: "d4", Sticky: &yes, Summary: "new", Evidence: ev(512, 512)}},
	}
	if err := out.Validate(in); err != nil {
		t.Fatalf("versionless update patch rejected: %v", err)
	}
}

func TestSelectBatchCountCap(t *testing.T) {
	msgs := make([]Message, 10)
	for i := range msgs {
		msgs[i] = Message{Seq: int64(i + 1), Role: "user", Text: "hello"}
	}
	batch, last, more := SelectBatch(msgs, BatchOptions{MaxMessages: 4, MaxInputTokens: 100000})
	if len(batch) != 4 || last != 4 || !more {
		t.Fatalf("count cap: len=%d last=%d more=%v", len(batch), last, more)
	}
}

func TestSelectBatchTokenCapAndTruncate(t *testing.T) {
	big := strings.Repeat("x", 1000)
	msgs := []Message{
		{Seq: 1, Role: "user", Text: big},
		{Seq: 2, Role: "user", Text: big},
		{Seq: 3, Role: "user", Text: big},
	}
	// ~250 tokens per message; budget fits ~2.
	batch, last, more := SelectBatch(msgs, BatchOptions{MaxMessages: 100, MaxInputTokens: 520, PerMessageChars: 100000})
	if len(batch) != 2 || last != 2 || !more {
		t.Fatalf("token cap: len=%d last=%d more=%v", len(batch), last, more)
	}
	// Truncation marker.
	if _, cut := TruncateText(big, 100); !cut {
		t.Fatalf("expected truncation")
	}
	out, _ := TruncateText(big, 100)
	if !strings.Contains(out, "truncated") {
		t.Fatalf("missing truncation marker")
	}
}

func TestSelectBatchAlwaysProgresses(t *testing.T) {
	// Single oversized message must still be returned (progress guarantee).
	msgs := []Message{{Seq: 1, Role: "user", Text: strings.Repeat("y", 100000)}}
	batch, last, more := SelectBatch(msgs, BatchOptions{MaxMessages: 10, MaxInputTokens: 10, PerMessageChars: 100})
	if len(batch) != 1 || last != 1 || more {
		t.Fatalf("progress: len=%d last=%d more=%v", len(batch), last, more)
	}
}

func TestLoadPrompt(t *testing.T) {
	p, used := LoadPrompt("")
	if used || !strings.Contains(p, "Consolidation Engine") {
		t.Fatalf("default prompt not loaded (used=%v)", used)
	}
	f := filepath.Join(t.TempDir(), "p.md")
	_ = os.WriteFile(f, []byte("CUSTOM PROMPT"), 0o600)
	p2, used2 := LoadPrompt(f)
	if !used2 || p2 != "CUSTOM PROMPT" {
		t.Fatalf("override not loaded (used=%v p=%q)", used2, p2)
	}
	// Unreadable override → fall back to default.
	p3, used3 := LoadPrompt(filepath.Join(t.TempDir(), "missing.md"))
	if used3 || !strings.Contains(p3, "Consolidation Engine") {
		t.Fatalf("missing override should fall back to default")
	}
}

func ev(a, b int64) store.Evidence { return store.Evidence{SeqStart: a, SeqEnd: b} }

// Normalize no longer repairs anything. The one repair it had downgraded an
// inferred memory the model marked active to review, and both the status field
// and the review state are gone — the model states type and nothing else, so
// there is no longer a field it can set to a value needing correction.
//
// The function survives because the repair-and-note path is the right shape for
// the next safely-correctable deviation, and its callers already handle an empty
// result. This test pins that it reports no notes and mutates nothing.
func TestOutput_Normalize_IsANoOp(t *testing.T) {
	out := Output{MemoryOps: []MemoryOp{
		{Op: "add", Domain: "d1", Type: "fact", Text: "a"},
		{Op: "add", Domain: "d1", Type: "event", Text: "b"},
		{Op: "supersede", OldID: "h1", Domain: "d1", Type: "operational", Text: "c"},
	}}
	before := append([]MemoryOp(nil), out.MemoryOps...)

	if notes := out.Normalize(); len(notes) != 0 {
		t.Errorf("Normalize reported %d notes, want none: %v", len(notes), notes)
	}
	for i := range before {
		if out.MemoryOps[i] != before[i] {
			t.Errorf("memory_ops[%d] was mutated: %+v -> %+v", i, before[i], out.MemoryOps[i])
		}
	}
}

// An override prompt seeded by an older ClawEh keeps working — it names types
// that still exist — so it degrades silently: the agent simply never records an
// event or operational memory, and nothing says so. PromptIsStale is what makes
// that visible.
func TestPromptIsStale(t *testing.T) {
	if PromptIsStale(DefaultPrompt()) {
		t.Error("the shipped prompt reports itself as stale")
	}
	old := `# WHAT IS MEMORY
A memory has exactly one type:
- ` + "`fact`" + ` — something true.
- ` + "`preference`" + ` — how the user likes things done.
- ` + "`rule`" + ` — a hard directive.
"status": "active|review", "source": "user_explicit|assistant_inferred"`
	if !PromptIsStale(old) {
		t.Error("a pre-redesign prompt was not detected as stale")
	}
	// A genuinely customised prompt that teaches the current types is not stale,
	// however much else the operator changed.
	custom := "Record an event for anything time-stamped, and operational for " +
		"your own bookkeeping. Never record anything about medical matters."
	if PromptIsStale(custom) {
		t.Error("a customised prompt naming the current types was called stale")
	}
}

// A retire may carry no evidence, because it removes a memory that already
// exists rather than asserting anything a message would have to justify.
//
// This is what makes housekeeping possible at all: merging two memories that
// say the same thing, or dropping one a newer memory contradicts, is tidying
// the current conversation never raised and so cannot cite. Requiring evidence
// there made every such op invalid — and one invalid op rejects the whole
// payload, so an agent following the rule would have aborted entire runs.
func TestRetireMayOmitEvidence(t *testing.T) {
	in := sampleInput()
	out := Output{MemoryOps: []MemoryOp{
		{Op: "retire", ID: "h9", Reason: "duplicate of h31"},
	}}
	if err := out.Validate(in); err != nil {
		t.Fatalf("retire without evidence rejected: %v", err)
	}
}

// Evidence given on a retire is still checked: omitting it is allowed, but
// pointing at a range outside the batch is a mistake either way.
func TestRetireWithBadEvidenceIsStillRejected(t *testing.T) {
	in := sampleInput()
	out := Output{MemoryOps: []MemoryOp{
		{Op: "retire", ID: "h9", Reason: "x", Evidence: ev(999, 999)},
	}}
	if err := out.Validate(in); err == nil {
		t.Error("out-of-range evidence on a retire was accepted")
	}
}

// The exemption is for retire ONLY. An add or supersede writes text, which is
// exactly what the evidence rule exists to keep anchored to a real message.
func TestAddAndSupersedeStillRequireEvidence(t *testing.T) {
	in := sampleInput()
	for _, op := range []MemoryOp{
		{Op: "add", Domain: "d4", Type: "fact", Text: "invented"},
		{Op: "supersede", OldID: "h9", Domain: "d4", Type: "rule", Text: "invented"},
	} {
		out := Output{MemoryOps: []MemoryOp{op}}
		if err := out.Validate(in); err == nil {
			t.Errorf("%s without evidence was accepted", op.Op)
		}
	}
}

// A retire still has to name a memory that exists — the id check is what makes
// dropping the evidence requirement safe.
func TestRetireStillNeedsAKnownID(t *testing.T) {
	in := sampleInput()
	out := Output{MemoryOps: []MemoryOp{{Op: "retire", ID: "hNOPE", Reason: "x"}}}
	if err := out.Validate(in); err == nil {
		t.Error("retire of an unknown memory was accepted")
	}
}

// The shipped prompt must actually ask for the housekeeping, and must ask for
// it the safe way: retiring duplicates rather than rewriting several distinct
// memories into one vaguer summary.
func TestDefaultPromptAsksForHousekeeping(t *testing.T) {
	p := DefaultPrompt()
	for _, want := range []string{
		"Tidy the domains you touch",
		"may omit `evidence`",
		"Retire; do not rewrite",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
}
