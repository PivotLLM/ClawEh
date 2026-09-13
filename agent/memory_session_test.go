// ClawEh - Cognitive Memory
// License: MIT

package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/cogmem/store"
	"github.com/PivotLLM/ClawEh/llmcontext"
	"github.com/PivotLLM/ClawEh/memory"
	"github.com/PivotLLM/ClawEh/providers"
)

func openSessionStore(t *testing.T, mem *memorySession) *store.Store {
	t.Helper()
	s, err := store.Open(mem.dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", mem.dbPath, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestMemorySession_ObserveFeedsInbox: the loop hands the memory session a
// copy of each stored message; meaningful roles land in the store's inbox
// under the transcript seq, tool plumbing does not.
func TestMemorySession_ObserveFeedsInbox(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	agent := al.registry.GetDefaultAgent()

	mem := al.wireCognitiveMemory(agent, "agent:main:main")
	if mem == nil {
		t.Fatal("default agent has cognitive memory on; expected a session")
	}
	defer mem.Close()
	ctx := context.Background()
	mem.Observe(ctx, 7, providers.Message{Role: "user", Content: "remember the blue door"})
	mem.Observe(ctx, 8, providers.Message{Role: "tool", Content: "plumbing"})
	mem.Observe(ctx, 9, providers.Message{Role: "assistant", Content: "noted"})

	s := openSessionStore(t, mem)
	rows, err := s.InboxRange(ctx, s.DB(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Seq != 7 || rows[1].Seq != 9 {
		t.Fatalf("inbox = %+v, want seqs 7 and 9", rows)
	}
}

// TestMemorySession_NilAndEphemeralAreNoOps: a nil session (agent without
// cogmem) and a sub-agent session (throwaway snapshot) never write an inbox.
func TestMemorySession_NilAndEphemeralAreNoOps(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	agent := al.registry.GetDefaultAgent()
	ctx := context.Background()

	var none *memorySession
	none.Observe(ctx, 1, providers.Message{Role: "user", Content: "x"})
	none.RecordToolUse("file_read_lines")
	if inj := none.Recall(ctx, "anything"); inj != nil {
		t.Fatalf("nil session recalled %+v", inj)
	}
	none.Close()

	sub := al.wireCognitiveMemory(agent, "subagent:abc123")
	if sub == nil || !sub.ephemeral {
		t.Fatalf("sub-agent session = %+v, want ephemeral", sub)
	}
	defer sub.Close()
	sub.Observe(ctx, 1, providers.Message{Role: "user", Content: "x"})
	s := openSessionStore(t, sub)
	if n, _ := s.InboxCount(ctx, s.DB()); n != 0 {
		t.Fatalf("ephemeral session wrote %d inbox rows", n)
	}
}

// TestMemorySession_RecallPlacesStickyInSystem: a memory in the sticky General
// domain comes back as a system-stable injection; the routed slot stays empty
// when nothing matches.
func TestMemorySession_RecallPlacesStickyInSystem(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	agent := al.registry.GetDefaultAgent()
	mem := al.wireCognitiveMemory(agent, "agent:main:main")
	defer mem.Close()
	ctx := context.Background()

	s := openSessionStore(t, mem)
	general, err := s.GeneralDomain(ctx, s.DB())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMemory(ctx, s.DB(), store.AddMemoryParams{
		DomainID: general.ID, Type: store.TypeRule, Text: "always sign off as Alice",
		Status: store.StatusActive, Confidence: 0.9,
	}); err != nil {
		t.Fatal(err)
	}

	inj := mem.Recall(ctx, "hello")
	var stable, routed int
	for _, i := range inj {
		switch i.Placement {
		case llmcontext.PlaceSystemStable:
			stable++
			if !strings.Contains(i.Text, "always sign off as Alice") {
				t.Fatalf("stable block missing the sticky memory: %q", i.Text)
			}
		case llmcontext.PlaceCurrentUser:
			routed++
		}
	}
	if stable != 1 || routed != 0 {
		t.Fatalf("injections = %+v, want one stable and no routed", inj)
	}
}

// TestMemorySession_BackfillsInboxFromArchiveOnce: on first open after the
// upgrade, archived messages past the watermark are copied into the inbox so
// nothing already spoken is lost to memory; a second open does not repeat it.
func TestMemorySession_BackfillsInboxFromArchiveOnce(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	agent := al.registry.GetDefaultAgent()
	const key = "agent:main:main"
	ctx := context.Background()

	// A store that consolidated through seq 1 before the inbox existed.
	pre, err := store.Open(store.SessionDBPath(agent.Workspace, key))
	if err != nil {
		t.Fatal(err)
	}
	if err := pre.SetWatermark(ctx, pre.DB(), store.InboxStateKey, 1, 1); err != nil {
		t.Fatal(err)
	}
	_ = pre.Close()

	// An archive holding seqs 1..4.
	a, err := memory.Open(archiveDBPath(agent.Workspace, key))
	if err != nil {
		t.Fatal(err)
	}
	for seq, m := range []providers.Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "tool", Content: "three"},
		{Role: "user", Content: "four"},
	} {
		if err := a.Append(int64(seq+1), m, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	_ = a.Close()

	mem := al.wireCognitiveMemory(agent, key)
	if mem.ensure() == nil {
		t.Fatal("store did not open")
	}
	s := openSessionStore(t, mem)
	rows, _ := s.InboxRange(ctx, s.DB(), 0, 100)
	if len(rows) != 2 || rows[0].Seq != 2 || rows[1].Seq != 4 {
		t.Fatalf("backfilled inbox = %+v, want seqs 2 and 4 (past watermark 1, meaningful only)", rows)
	}
	mem.Close()

	// Second open: flag set, nothing copied again even though the inbox is empty.
	if _, err := s.DeleteInboxThrough(ctx, s.DB(), 100); err != nil {
		t.Fatal(err)
	}
	again := al.wireCognitiveMemory(agent, key)
	again.ensure()
	defer again.Close()
	if n, _ := s.InboxCount(ctx, s.DB()); n != 0 {
		t.Fatalf("backfill ran twice: %d rows", n)
	}
}

// TestMemorySession_RecordToolUseRing: newest first, deduped, capped.
func TestMemorySession_RecordToolUseRing(t *testing.T) {
	m := &memorySession{}
	for i := 0; i < maxRecentTools+3; i++ {
		m.RecordToolUse("t" + string(rune('a'+i)))
	}
	m.RecordToolUse("ta") // re-used: moves to front, no duplicate
	got := m.recentToolsSnapshot()
	if len(got) != maxRecentTools || got[0] != "ta" {
		t.Fatalf("ring = %v", got)
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Fatalf("duplicate %q in ring %v", n, got)
		}
		seen[n] = true
	}
}
