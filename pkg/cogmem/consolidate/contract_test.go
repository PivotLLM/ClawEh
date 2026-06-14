// ClawEh - Cognitive Memory
// License: MIT

package consolidate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
)

func sampleInput() Input {
	return Input{
		CurrentState: CurrentState{Domains: []DomainView{{
			ID: "d4", Type: "project", Name: "Layout", Status: "active", Version: 2,
			Hooks: []HookView{{ID: "h9", Kind: "rule", Text: "Never use the color blue.", Confidence: 0.9}},
		}}},
		NewMessages: []Message{{Seq: 512, Role: "user", Text: "Actually, use blue for the layout."}},
	}
}

func TestValidateHappyPath(t *testing.T) {
	in := sampleInput()
	out := Output{
		HookOps: []HookOp{{
			Op: "supersede", OldID: "h9", Domain: "d4", Kind: "rule",
			Text: "Use blue for the layout.", Confidence: 0.95, Status: "active",
			Source: "user_explicit", Evidence: ev(512, 512),
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
		"evidence out of range": {HookOps: []HookOp{{Op: "add", Domain: "d4", Kind: "fact", Text: "ok", Source: "user_explicit", Status: "active", Evidence: ev(999, 999)}}},
		"unknown domain":        {HookOps: []HookOp{{Op: "add", Domain: "dX", Kind: "fact", Text: "ok", Source: "user_explicit", Status: "active", Evidence: ev(512, 512)}}},
		"unknown retire id":     {HookOps: []HookOp{{Op: "retire", ID: "hZ", Reason: "x", Evidence: ev(512, 512)}}},
		"invalid kind":          {HookOps: []HookOp{{Op: "add", Domain: "d4", Kind: "bogus", Text: "ok", Source: "user_explicit", Status: "active", Evidence: ev(512, 512)}}},
		"inferred active":       {HookOps: []HookOp{{Op: "add", Domain: "d4", Kind: "fact", Text: "ok", Source: "assistant_inferred", Status: "active", Evidence: ev(512, 512)}}},
		"secret in text":        {HookOps: []HookOp{{Op: "add", Domain: "d4", Kind: "fact", Text: "api_key = sk-abcdefghij0123456789", Source: "user_explicit", Status: "active", Evidence: ev(512, 512)}}},
		"update no version":     {DomainOps: []DomainOp{{Op: "update", ID: "d4", Evidence: ev(512, 512)}}},
		"create no tmp_id":      {DomainOps: []DomainOp{{Op: "create", Type: "project", Name: "X", Evidence: ev(512, 512)}}},
	}
	for name, out := range cases {
		if err := out.Validate(in); err == nil {
			t.Errorf("%s: expected rejection, got nil", name)
		}
	}
}

func TestValidateTmpIDReference(t *testing.T) {
	in := sampleInput()
	out := Output{
		DomainOps: []DomainOp{{Op: "create", TmpID: "t1", Type: "project", Name: "New", Status: "active", Evidence: ev(512, 512)}},
		HookOps:   []HookOp{{Op: "add", Domain: "t1", Kind: "fact", Text: "a fact", Source: "user_explicit", Status: "active", Evidence: ev(512, 512)}},
	}
	if err := out.Validate(in); err != nil {
		t.Fatalf("tmp_id reference rejected: %v", err)
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
