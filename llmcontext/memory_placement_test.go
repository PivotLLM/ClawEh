// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/providers"
)

// stubBuilder is a MessageBuilder that emits a system message plus the history
// verbatim, which is all these tests need to see where blocks land.
type stubBuilder struct{}

func (stubBuilder) BuildMessages(history []providers.Message, summary, current string, _ []string, _, _ string) []providers.Message {
	out := []providers.Message{{Role: "system", Content: "SYSTEM" + summary}}
	out = append(out, history...)
	if current != "" {
		out = append(out, providers.Message{Role: "user", Content: current})
	}
	return out
}

// memMgr builds a Manager wired to a store and a memory-block callback.
func memMgr(t *testing.T, store *mockStore, stable, routed string) *Manager {
	t.Helper()
	m := New("test-session", store, stubBuilder{}, nil, WithContextWindow(100_000)).(*Manager)
	m.SetMemoryBlocks(func(string, []string, string) (string, string) { return stable, routed })
	return m
}

// TestRoutedMemory_RidesOnTheCurrentTurn is the placement this change exists
// for. ROUTED is selected from the latest user message, so it must not sit in
// the system message: that precedes the entire history, and anything volatile
// there invalidates the cached prefix for all of it.
func TestRoutedMemory_RidesOnTheCurrentTurn(t *testing.T) {
	store := newMockStore()
	store.SetHistory("test-session", []providers.Message{
		{Role: "user", Content: "older question"},
		{Role: "assistant", Content: "older answer"},
		{Role: "user", Content: "current question"},
	})

	msgs, err := memMgr(t, store, "STABLEBLOCK", "ROUTEDBLOCK").Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !strings.Contains(msgs[0].Content, "STABLEBLOCK") {
		t.Error("stable memory belongs in the system message (cached prefix)")
	}
	if strings.Contains(msgs[0].Content, "ROUTEDBLOCK") {
		t.Error("routed memory must NOT be in the system message — it breaks the cached prefix for the whole history")
	}

	last := msgs[len(msgs)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "current question") {
		t.Fatalf("expected the latest user turn last, got %+v", last)
	}
	if !strings.Contains(last.Content, "ROUTEDBLOCK") {
		t.Errorf("routed memory should ride on the current turn, got:\n%s", last.Content)
	}
	// It must attach to the LATEST user turn, not an earlier one.
	if strings.Contains(msgs[1].Content, "ROUTEDBLOCK") {
		t.Error("routed memory attached to an older user turn")
	}
}

// TestRoutedMemory_NeverPersisted is the load-bearing safety property: the
// injected block lives only in the built slice. If it reached the store, history
// would gain one stale memory dump per turn, silently and cumulatively.
func TestRoutedMemory_NeverPersisted(t *testing.T) {
	store := newMockStore()
	store.SetHistory("test-session", []providers.Message{{Role: "user", Content: "question"}})

	mgr := memMgr(t, store, "STABLEBLOCK", "ROUTEDBLOCK")
	for i := 0; i < 3; i++ {
		if _, err := mgr.Build(context.Background()); err != nil {
			t.Fatalf("Build: %v", err)
		}
	}

	for _, sm := range store.GetHistoryWithSeqs("test-session") {
		if strings.Contains(sm.Content, "ROUTEDBLOCK") || strings.Contains(sm.Content, "STABLEBLOCK") {
			t.Fatalf("memory block leaked into stored history: %q", sm.Content)
		}
	}
}

// TestRoutedMemory_StableAcrossRepeatedBuilds guards the cache property: the
// same inputs must produce a byte-identical system message every time, or the
// prefix breaks on every dispatch of the turn.
func TestRoutedMemory_StableAcrossRepeatedBuilds(t *testing.T) {
	store := newMockStore()
	store.SetHistory("test-session", []providers.Message{{Role: "user", Content: "question"}})
	mgr := memMgr(t, store, "STABLEBLOCK", "ROUTEDBLOCK")

	first, _ := mgr.Build(context.Background())
	second, _ := mgr.Build(context.Background())

	if first[0].Content != second[0].Content {
		t.Errorf("system message differs between builds:\n%q\nvs\n%q", first[0].Content, second[0].Content)
	}
	if got := strings.Count(second[len(second)-1].Content, "ROUTEDBLOCK"); got != 1 {
		t.Errorf("routed block appended %d times on the second build, want 1", got)
	}
}

// TestRoutedMemory_NoUserTurnDropsBlock covers a turn that opens on tool
// plumbing: with nowhere valid to put the block, dropping it beats forcing a
// message shape a provider will reject. The next user turn re-routes it.
func TestRoutedMemory_NoUserTurnDropsBlock(t *testing.T) {
	store := newMockStore()
	store.SetHistory("test-session", []providers.Message{
		{Role: "assistant", Content: "thinking"},
		{Role: "tool", ToolCallID: "t1", Content: "tool output"},
	})

	msgs, err := memMgr(t, store, "STABLEBLOCK", "ROUTEDBLOCK").Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "ROUTEDBLOCK") {
			t.Errorf("routed block should be dropped when there is no user turn, found in %s: %q", m.Role, m.Content)
		}
	}
}

// TestAttachRoutedMemory_EmptyIsNoop keeps a non-cognitive agent's slice
// untouched.
func TestAttachRoutedMemory_EmptyIsNoop(t *testing.T) {
	msgs := []providers.Message{{Role: "user", Content: "question"}}
	attachRoutedMemory(msgs, "")
	if msgs[0].Content != "question" {
		t.Errorf("empty routed block modified the turn: %q", msgs[0].Content)
	}
}
