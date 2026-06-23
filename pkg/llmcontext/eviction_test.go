// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// --- test builders -------------------------------------------------------

// evToolCall builds a stored ToolCall (Function-form, as it is persisted) for a
// reader/writer tool with the given args.
func evToolCall(id, tool string, args map[string]any) providers.ToolCall {
	b, _ := json.Marshal(args)
	return providers.ToolCall{ID: id, Function: &providers.FunctionCall{Name: tool, Arguments: string(b)}}
}

// turnSpec describes one turn group in a synthetic history (oldest first).
type turnSpec struct {
	// tool turn: assistant(ToolCalls=[tc]) + tool result with content.
	tool    string
	args    map[string]any
	id      string
	content string
	// text turn: a single assistant text message (tool == "").
	text string
}

// buildHistory turns specs (oldest→newest) into a stored slice with seqs. Each
// tool spec is two messages (one turn group); each text spec is one.
func buildHistory(specs ...turnSpec) []memory.StoredMessage {
	var msgs []providers.Message
	for _, s := range specs {
		if s.tool == "" {
			msgs = append(msgs, providers.Message{Role: "assistant", Content: s.text})
			continue
		}
		id := s.id
		if id == "" {
			id = s.tool + "-" + s.content[:min(4, len(s.content))]
		}
		msgs = append(msgs,
			providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{evToolCall(id, s.tool, s.args)}},
			providers.Message{Role: "tool", ToolCallID: id, Content: s.content},
		)
	}
	out := make([]memory.StoredMessage, len(msgs))
	for i, m := range msgs {
		out[i] = memory.StoredMessage{Seq: int64(i + 1), Message: m}
	}
	return out
}

func newEvictMgr(store *seqStore, p EvictionPolicy) ContextManager {
	// WithContextWindow(0) disables the derived budget so non-budget tests are
	// deterministic; budget tests set BudgetBytes explicitly.
	return New("sess", store, nil, nil, WithContextWindow(0), WithEvictionPolicy(p))
}

// findToolResult returns the content of the tool result for the given call id.
func findToolResult(store *seqStore, id string) string {
	for _, sm := range store.GetHistoryWithSeqs("sess") {
		if sm.Role == "tool" && sm.ToolCallID == id {
			return sm.Content
		}
	}
	return ""
}

func basePolicy() EvictionPolicy {
	return EvictionPolicy{Enabled: true, ProtectTurns: 3, LargeTurns: 5, LargeSize: 100, EvictTurns: 10, BudgetBytes: 0}
}

// --- tests ---------------------------------------------------------------

func TestSweep_Disabled(t *testing.T) {
	p := basePolicy()
	p.Enabled = false
	store := newSeqStore(buildHistory(
		turnSpec{tool: "file_read", id: "r", args: map[string]any{"path": "a.md"}, content: strings.Repeat("x", 500)},
		turnSpec{text: "1"}, turnSpec{text: "2"}, turnSpec{text: "3"},
		turnSpec{text: "4"}, turnSpec{text: "5"}, turnSpec{text: "6"},
	))
	events := newEvictMgr(store, p).SweepEvictions(context.Background())
	if events != nil {
		t.Fatalf("disabled policy must not evict; got %d events", len(events))
	}
	if got := findToolResult(store, "r"); isEvicted(got) {
		t.Fatalf("content evicted while disabled")
	}
}

func TestSweep_ProtectsRecent(t *testing.T) {
	// A large read in the last protect (3) turns must survive.
	store := newSeqStore(buildHistory(
		turnSpec{text: "old1"}, turnSpec{text: "old2"},
		turnSpec{tool: "file_read", id: "r", args: map[string]any{"path": "a.md"}, content: strings.Repeat("x", 500)}, // age 3
		turnSpec{text: "n1"}, turnSpec{text: "n2"},
	))
	events := newEvictMgr(store, basePolicy()).SweepEvictions(context.Background())
	if len(events) != 0 {
		t.Fatalf("protected read evicted: %+v", events)
	}
	if isEvicted(findToolResult(store, "r")) {
		t.Fatalf("protected read content evicted")
	}
}

func TestSweep_Large(t *testing.T) {
	// Read at age 6 (> large 5), size >= 100 → evicted "large".
	store := newSeqStore(buildHistory(
		turnSpec{text: "0"}, turnSpec{text: "1"},
		turnSpec{tool: "file_read", id: "r", args: map[string]any{"path": "ch17.md"}, content: strings.Repeat("x", 200)}, // age 6
		turnSpec{text: "3"}, turnSpec{text: "4"}, turnSpec{text: "5"}, turnSpec{text: "6"}, turnSpec{text: "7"},
	))
	events := newEvictMgr(store, basePolicy()).SweepEvictions(context.Background())
	if len(events) != 1 || events[0].Reason != "large" {
		t.Fatalf("want 1 large eviction, got %+v", events)
	}
	e := events[0]
	if e.Tool != "file_read" || e.Resource != "ch17.md" || e.Bytes != 200 || e.AgeTurns != 6 {
		t.Fatalf("event fields wrong: %+v", e)
	}
	if !isEvicted(findToolResult(store, "r")) {
		t.Fatalf("large read content not evicted")
	}
}

func TestSweep_SmallNotEvictedEarly(t *testing.T) {
	// Small read (< largeSize) at age 6 (between large and evict) stays.
	store := newSeqStore(buildHistory(
		turnSpec{text: "0"}, turnSpec{text: "1"},
		turnSpec{tool: "file_read", id: "r", args: map[string]any{"path": "a.md"}, content: "tiny"}, // age 6, 4 bytes
		turnSpec{text: "3"}, turnSpec{text: "4"}, turnSpec{text: "5"}, turnSpec{text: "6"}, turnSpec{text: "7"},
	))
	events := newEvictMgr(store, basePolicy()).SweepEvictions(context.Background())
	if len(events) != 0 {
		t.Fatalf("small read evicted early: %+v", events)
	}
}

func TestSweep_StaleAnySize(t *testing.T) {
	// Small read at age 12 (> evict 10) → evicted "stale" regardless of size.
	specs := []turnSpec{{tool: "file_read", id: "r", args: map[string]any{"path": "a.md"}, content: "tiny"}}
	for i := 0; i < 11; i++ { // 11 newer text turns ⇒ read age 12
		specs = append(specs, turnSpec{text: "t"})
	}
	store := newSeqStore(buildHistory(specs...))
	events := newEvictMgr(store, basePolicy()).SweepEvictions(context.Background())
	if len(events) != 1 || events[0].Reason != "stale" {
		t.Fatalf("want 1 stale eviction, got %+v", events)
	}
	if !isEvicted(findToolResult(store, "r")) {
		t.Fatalf("stale read content not evicted")
	}
}

func TestSweep_SupersededByReread(t *testing.T) {
	// Old small read of a.md, later re-read of a.md (outside protect) → old one
	// evicted "superseded" despite being small.
	store := newSeqStore(buildHistory(
		turnSpec{tool: "file_read", id: "old", args: map[string]any{"path": "a.md"}, content: "tiny"}, // age 5
		turnSpec{text: "1"}, turnSpec{text: "2"}, turnSpec{text: "3"},
		turnSpec{tool: "file_read", id: "new", args: map[string]any{"path": "a.md"}, content: "fresh"}, // age 1
	))
	events := newEvictMgr(store, basePolicy()).SweepEvictions(context.Background())
	// The evicted message is the tool *result* (seq 2: assistant=1, result=2).
	if len(events) != 1 || events[0].Reason != "superseded" || events[0].Seq != 2 {
		t.Fatalf("want old read superseded, got %+v", events)
	}
	if !isEvicted(findToolResult(store, "old")) {
		t.Fatalf("superseded read not evicted")
	}
	if isEvicted(findToolResult(store, "new")) {
		t.Fatalf("fresh read wrongly evicted")
	}
}

func TestSweep_SupersededByEdit(t *testing.T) {
	store := newSeqStore(buildHistory(
		turnSpec{tool: "file_read", id: "r", args: map[string]any{"path": "a.md"}, content: "tiny"}, // age 5
		turnSpec{text: "1"}, turnSpec{text: "2"}, turnSpec{text: "3"},
		turnSpec{tool: "file_edit", id: "e", args: map[string]any{"path": "a.md"}, content: "ok"}, // age 1, writer
	))
	events := newEvictMgr(store, basePolicy()).SweepEvictions(context.Background())
	if len(events) != 1 || events[0].Reason != "superseded" {
		t.Fatalf("want read superseded by edit, got %+v", events)
	}
}

func TestSweep_ProtectBeatsSuperseded(t *testing.T) {
	// Read within protect, then edited — protect wins, read stays.
	store := newSeqStore(buildHistory(
		turnSpec{tool: "file_read", id: "r", args: map[string]any{"path": "a.md"}, content: strings.Repeat("x", 500)}, // age 3
		turnSpec{text: "1"},
		turnSpec{tool: "file_edit", id: "e", args: map[string]any{"path": "a.md"}, content: "ok"}, // age 1
	))
	events := newEvictMgr(store, basePolicy()).SweepEvictions(context.Background())
	if len(events) != 0 {
		t.Fatalf("protected read evicted despite supersession: %+v", events)
	}
}

func TestSweep_Budget(t *testing.T) {
	// Three reads at ages 4–6, none large/stale/superseded → budget candidates.
	// Sizes 250+200+150=600 > budget 300 ⇒ evict largest-first (250, 200), keep 150.
	p := basePolicy()
	p.LargeTurns = 100 // disable the large tier
	p.EvictTurns = 100 // disable the stale tier
	p.LargeSize = 1 << 20
	p.BudgetBytes = 300
	store := newSeqStore(buildHistory(
		turnSpec{tool: "file_read", id: "a", args: map[string]any{"path": "a.md"}, content: strings.Repeat("x", 250)}, // age 6
		turnSpec{tool: "file_read", id: "b", args: map[string]any{"path": "b.md"}, content: strings.Repeat("x", 200)}, // age 5
		turnSpec{tool: "file_read", id: "c", args: map[string]any{"path": "c.md"}, content: strings.Repeat("x", 150)}, // age 4
		turnSpec{text: "1"}, turnSpec{text: "2"}, turnSpec{text: "3"},                                                 // ages 1-3 protected, no reader bytes
	))
	events := newEvictMgr(store, p).SweepEvictions(context.Background())
	if len(events) != 2 {
		t.Fatalf("want 2 budget evictions, got %+v", events)
	}
	for _, e := range events {
		if e.Reason != "budget" {
			t.Fatalf("non-budget reason: %+v", e)
		}
	}
	if !isEvicted(findToolResult(store, "a")) || !isEvicted(findToolResult(store, "b")) {
		t.Fatalf("largest reads not evicted")
	}
	if isEvicted(findToolResult(store, "c")) {
		t.Fatalf("smallest read evicted under budget")
	}
}

func TestSweep_NonReaderUntouched(t *testing.T) {
	// A non-reader tool result (msg_send) old + large is never evicted.
	store := newSeqStore(buildHistory(
		turnSpec{text: "0"}, turnSpec{text: "1"},
		turnSpec{tool: "msg_send", id: "m", args: map[string]any{"text": "hi"}, content: strings.Repeat("x", 5000)}, // age 6
		turnSpec{text: "3"}, turnSpec{text: "4"}, turnSpec{text: "5"}, turnSpec{text: "6"}, turnSpec{text: "7"},
	))
	events := newEvictMgr(store, basePolicy()).SweepEvictions(context.Background())
	if len(events) != 0 {
		t.Fatalf("non-reader evicted: %+v", events)
	}
}

func TestSweep_Idempotent(t *testing.T) {
	store := newSeqStore(buildHistory(
		turnSpec{text: "0"}, turnSpec{text: "1"},
		turnSpec{tool: "file_read", id: "r", args: map[string]any{"path": "a.md"}, content: strings.Repeat("x", 200)},
		turnSpec{text: "3"}, turnSpec{text: "4"}, turnSpec{text: "5"}, turnSpec{text: "6"}, turnSpec{text: "7"},
	))
	mgr := newEvictMgr(store, basePolicy())
	if got := mgr.SweepEvictions(context.Background()); len(got) != 1 {
		t.Fatalf("first sweep want 1, got %d", len(got))
	}
	if got := mgr.SweepEvictions(context.Background()); len(got) != 0 {
		t.Fatalf("second sweep must be no-op, got %+v", got)
	}
}

func TestSweep_EvictedStaysEvictedAcrossTiers(t *testing.T) {
	// A large read is evicted via the "large" tier at age 6. After more turns
	// push it past evict_turns (10), the sweep must recognize it is already
	// evicted and leave it alone — no re-eviction, no duplicate notice.
	store := newSeqStore(buildHistory(
		turnSpec{text: "0"}, turnSpec{text: "1"},
		turnSpec{tool: "file_read", id: "r", args: map[string]any{"path": "a.md"}, content: strings.Repeat("x", 200)}, // age 6
		turnSpec{text: "3"}, turnSpec{text: "4"}, turnSpec{text: "5"}, turnSpec{text: "6"}, turnSpec{text: "7"},
	))
	mgr := newEvictMgr(store, basePolicy())

	first := mgr.SweepEvictions(context.Background())
	if len(first) != 1 || first[0].Reason != "large" {
		t.Fatalf("first sweep: want 1 large eviction, got %+v", first)
	}
	placeholder := findToolResult(store, "r")
	if !isEvicted(placeholder) {
		t.Fatalf("read not evicted on first sweep")
	}

	// Age the (already-evicted) read past evict_turns by appending more turns.
	aged := store.GetHistoryWithSeqs("sess")
	base := aged[len(aged)-1].Seq
	for i := 0; i < 6; i++ {
		aged = append(aged, memory.StoredMessage{
			Seq:     base + int64(i+1),
			Message: providers.Message{Role: "assistant", Content: "more"},
		})
	}
	store.SetHistoryWithSeqs("sess", aged)

	second := mgr.SweepEvictions(context.Background())
	if len(second) != 0 {
		t.Fatalf("already-evicted read re-evicted after aging: %+v", second)
	}
	if got := findToolResult(store, "r"); got != placeholder {
		t.Fatalf("placeholder changed on re-sweep:\n  before: %q\n  after:  %q", placeholder, got)
	}
}

func TestEvictionEvent_String(t *testing.T) {
	e := EvictionEvent{Tool: "file_read", Resource: "files/ch17.md", Bytes: 18432, AgeTurns: 6, Reason: "large"}
	want := "[Evicted 18432 bytes at 6 turns (large): file_read files/ch17.md]"
	if got := e.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestEvictionEvent_URLCap(t *testing.T) {
	longURL := "https://example.com/" + strings.Repeat("a", 200)
	e := EvictionEvent{Tool: "web_fetch", Resource: longURL, Bytes: 100, AgeTurns: 7, Reason: "stale"}
	got := e.String()
	if !strings.Contains(got, "…") {
		t.Fatalf("long URL not capped: %q", got)
	}
	if strings.Contains(got, strings.Repeat("a", 200)) {
		t.Fatalf("full long URL leaked into notice")
	}
}
