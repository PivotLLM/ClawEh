// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/memory"
	"github.com/PivotLLM/ClawEh/providers"
)

// agingStore is a mockStore whose GetHistoryWithSeqs stamps CreatedAt, which the
// plain mockStore leaves zero. Age-sensitive trigger tests need real timestamps;
// everything else deliberately does not.
type agingStore struct {
	*mockStore
	ages map[int]time.Duration // index → age of that message; missing = now
}

func newAgingStore() *agingStore {
	return &agingStore{mockStore: newMockStore(), ages: map[int]time.Duration{}}
}

func (s *agingStore) GetHistoryWithSeqs(key string) []memory.StoredMessage {
	src := s.history[key]
	out := make([]memory.StoredMessage, len(src))
	now := time.Now()
	for i, msg := range src {
		out[i] = memory.StoredMessage{Seq: int64(i + 1), CreatedAt: now.Add(-s.ages[i]), Message: msg}
	}
	return out
}

// TestTrigger_AgeFiresBelowFloor is the fix for the headline symptom: a session
// whose history is weeks old but occupies almost none of the context window.
// Every percentage trigger is gated by the floor and the count trigger never
// arrives, so before the age trigger such a session never compacted at all.
func TestTrigger_AgeFiresBelowFloor(t *testing.T) {
	store := newAgingStore()
	mgr := newTestManager(store,
		WithContextWindow(1_000_000), // tiny history => far below the floor
		WithMinPercent(20),
		WithNormalPercent(50),
		WithSafetyPercent(80),
		WithMessageThreshold(0), // count trigger off: age must do this alone
		WithTriggerDays(7),
		WithRetainMaxAgeDays(5),
	)

	safetyNet, called := false, false
	mgr.SetTestCompressHook(func(s bool) { called, safetyNet = true, s })

	store.ages[0] = 30 * 24 * time.Hour // oldest message is a month old
	if err := mgr.AddUserMessage(context.Background(), msgWithContent("hi")); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	if !called {
		t.Fatal("age trigger must fire below the percentage floor")
	}
	if safetyNet {
		t.Error("age trigger is a routine compaction, not the emergency path")
	}
}

// TestTrigger_AgeDoesNotFireWhenFresh is the off path: the same configuration
// with recent history must not compact.
func TestTrigger_AgeDoesNotFireWhenFresh(t *testing.T) {
	store := newAgingStore()
	mgr := newTestManager(store,
		WithContextWindow(1_000_000),
		WithMinPercent(20),
		WithMessageThreshold(0),
		WithTriggerDays(7),
		WithRetainMaxAgeDays(5),
	)
	called := false
	mgr.SetTestCompressHook(func(_ bool) { called = true })

	store.ages[0] = 2 * 24 * time.Hour // well inside the trigger
	if err := mgr.AddUserMessage(context.Background(), msgWithContent("hi")); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	if called {
		t.Error("age trigger fired on a two-day-old session with trigger.days=7")
	}
}

// TestTrigger_AgeDisabled verifies 0 means off, which is the setting a
// performance-first deployment would choose.
func TestTrigger_AgeDisabled(t *testing.T) {
	store := newAgingStore()
	mgr := newTestManager(store,
		WithContextWindow(1_000_000),
		WithMinPercent(20),
		WithMessageThreshold(0),
		WithTriggerDays(0),
	)
	called := false
	mgr.SetTestCompressHook(func(_ bool) { called = true })

	store.ages[0] = 365 * 24 * time.Hour
	if err := mgr.AddUserMessage(context.Background(), msgWithContent("hi")); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	if called {
		t.Error("trigger.days=0 must disable the age trigger entirely")
	}
}

// TestTrigger_AgeIgnoresSystemMessage guards against a false positive: a
// session's system message is rewritten in place and keeps its original
// timestamp, so judging staleness by it would compact every long-lived session
// on every message forever.
func TestTrigger_AgeIgnoresSystemMessage(t *testing.T) {
	store := newAgingStore()
	store.history["test-session"] = []providers.Message{{Role: "system", Content: "prompt"}}
	store.ages[0] = 90 * 24 * time.Hour // ancient system message
	store.ages[1] = time.Minute         // fresh conversation

	mgr := newTestManager(store,
		WithContextWindow(1_000_000),
		WithMinPercent(20),
		WithMessageThreshold(0),
		WithTriggerDays(7),
	)
	called := false
	mgr.SetTestCompressHook(func(_ bool) { called = true })

	if err := mgr.AddUserMessage(context.Background(), msgWithContent("hi")); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	if called {
		t.Error("an old system message must not make a fresh conversation look stale")
	}
}

// TestOldestAge_NoUsableTimestamp covers histories written before CreatedAt
// existed: with nothing to measure, the age trigger must stay silent rather than
// treat a zero time as 1970.
func TestOldestAge_NoUsableTimestamp(t *testing.T) {
	stored := []memory.StoredMessage{{Message: providers.Message{Role: "user", Content: "x"}}}
	if _, ok := oldestAge(stored, time.Now()); ok {
		t.Error("a zero CreatedAt must report no usable age")
	}
	if _, ok := oldestAge(nil, time.Now()); ok {
		t.Error("an empty window must report no usable age")
	}
}

// TestSelectTail_AgeCapDropsOldGroups verifies the retention half: an age
// trigger that fired but retained everything would be a no-op pass.
func TestSelectTail_AgeCapDropsOldGroups(t *testing.T) {
	history := []providers.Message{
		msg("user", "ancient"),
		msg("assistant", "ancient reply"),
		msg("user", "recent"),
		msg("assistant", "recent reply"),
	}
	stored := storedAt(history, 30, 30, 1, 1) // days old
	tail, start := selectTail(stored, 0, 0, 5*24*time.Hour, testNow, estimateTokens)

	if len(tail) != 2 {
		t.Fatalf("want the 2 recent messages retained, got %d", len(tail))
	}
	if tail[0].Content != "recent" || tail[1].Content != "recent reply" {
		t.Errorf("wrong messages retained: %+v", storedToPlain(tail))
	}
	if start != 2 {
		t.Errorf("start = %d, want 2 (the two ancient messages go to the summary)", start)
	}
}

// TestSelectTail_AgeCapRespectsMinFloor verifies the floor still wins. Without
// this an idle session could have its entire window summarized away, leaving the
// model with no conversation at all.
func TestSelectTail_AgeCapRespectsMinFloor(t *testing.T) {
	history := []providers.Message{
		msg("user", "one"),
		msg("assistant", "two"),
		msg("user", "three"),
	}
	stored := storedAt(history, 90, 90, 90) // everything far past the cap
	tail, _ := selectTail(stored, 0, 2, 5*24*time.Hour, testNow, estimateTokens)
	if len(tail) < 2 {
		t.Fatalf("min-messages floor must override the age cap; got %d messages", len(tail))
	}
}

// TestSelectTail_AgeCapDisabled is the off path: maxAge 0 retains by budget only.
func TestSelectTail_AgeCapDisabled(t *testing.T) {
	history := []providers.Message{msg("user", "old"), msg("assistant", "older")}
	stored := storedAt(history, 900, 900)
	tail, start := selectTail(stored, 0, 0, 0, testNow, estimateTokens)
	if len(tail) != 2 || start != 0 {
		t.Errorf("maxAge=0 must disable the age cap; got %d messages, start %d", len(tail), start)
	}
}

// TestSelectTail_ZeroTimestampNotAged pins the safe direction for histories with
// no timestamps: never evict on age we cannot measure.
func TestSelectTail_ZeroTimestampNotAged(t *testing.T) {
	stored := []memory.StoredMessage{
		{Seq: 1, Message: msg("user", "no timestamp")},
		{Seq: 2, Message: msg("assistant", "also none")},
	}
	tail, _ := selectTail(stored, 0, 0, time.Hour, testNow, estimateTokens)
	if len(tail) != 2 {
		t.Errorf("zero timestamps must never be treated as old; got %d messages", len(tail))
	}
}
