// ClawEh
// License: MIT

package llmcontext

import (
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// TestPruneArchive_HonorsMessageCount verifies pruneArchive keeps the newest N
// messages when archiveMessageCount > 0.
func TestPruneArchive_HonorsMessageCount(t *testing.T) {
	const key = "prune-session"
	store := newResetStore(key)
	mgr := newResetManager(store, key, WithArchiveDir(t.TempDir()), WithArchiveMessageCount(3))

	for i := 1; i <= 10; i++ {
		mgr.archiveAppend(int64(i), providers.Message{Role: "user", Content: "m"})
	}

	mgr.pruneArchive(mgr.getOrOpenArchive())

	minSeq, maxSeq, err := mgr.getOrOpenArchive().Bounds()
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}
	if minSeq != 8 || maxSeq != 10 {
		t.Fatalf("bounds = [%d,%d], want [8,10]", minSeq, maxSeq)
	}
}

// TestPruneArchive_HonorsMessageDays verifies pruneArchive deletes messages
// older than archiveDays.
func TestPruneArchive_HonorsMessageDays(t *testing.T) {
	const key = "prune-session"
	store := newResetStore(key)
	mgr := newResetManager(store, key, WithArchiveDir(t.TempDir()), WithArchiveDays(2))

	a := mgr.getOrOpenArchive()
	now := time.Now()
	// seq 1: 5 days old (pruned). seq 2: today (kept).
	if err := a.Append(1, providers.Message{Role: "user", Content: "old"}, now.AddDate(0, 0, -5)); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	if err := a.Append(2, providers.Message{Role: "user", Content: "new"}, now); err != nil {
		t.Fatalf("Append 2: %v", err)
	}

	mgr.pruneArchive(a)

	minSeq, maxSeq, err := a.Bounds()
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}
	if minSeq != 2 || maxSeq != 2 {
		t.Fatalf("bounds = [%d,%d], want [2,2]", minSeq, maxSeq)
	}
}

// TestPruneArchive_HonorsSummaryCount verifies pruneArchive keeps the newest N
// summaries when summaryMaxCount > 0.
func TestPruneArchive_HonorsSummaryCount(t *testing.T) {
	const key = "prune-session"
	store := newResetStore(key)
	mgr := newResetManager(store, key, WithArchiveDir(t.TempDir()), WithSummaryMaxCount(2))

	a := mgr.getOrOpenArchive()
	for i := 0; i < 5; i++ {
		if _, err := a.AppendSummary(memory.SummaryRecord{GeneratedAt: time.Now(), Summary: "s"}); err != nil {
			t.Fatalf("AppendSummary %d: %v", i, err)
		}
	}

	mgr.pruneArchive(a)

	metas, err := a.ListSummaries()
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("summaries = %d, want 2", len(metas))
	}
}

// TestPruneArchive_HonorsSummaryDays verifies pruneArchive deletes summaries
// older than summaryRetentionDays.
func TestPruneArchive_HonorsSummaryDays(t *testing.T) {
	const key = "prune-session"
	store := newResetStore(key)
	mgr := newResetManager(store, key, WithArchiveDir(t.TempDir()), WithSummaryRetentionDays(2))

	a := mgr.getOrOpenArchive()
	now := time.Now()
	if _, err := a.AppendSummary(memory.SummaryRecord{GeneratedAt: now.AddDate(0, 0, -10), Summary: "old"}); err != nil {
		t.Fatalf("AppendSummary old: %v", err)
	}
	if _, err := a.AppendSummary(memory.SummaryRecord{GeneratedAt: now, Summary: "new"}); err != nil {
		t.Fatalf("AppendSummary new: %v", err)
	}

	mgr.pruneArchive(a)

	metas, err := a.ListSummaries()
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("summaries = %d, want 1", len(metas))
	}
}

// TestPruneArchive_ZeroConfigNoOp verifies that with all four caps at 0
// (defaults), pruneArchive deletes nothing.
func TestPruneArchive_ZeroConfigNoOp(t *testing.T) {
	const key = "prune-session"
	store := newResetStore(key)
	mgr := newResetManager(store, key, WithArchiveDir(t.TempDir()))

	a := mgr.getOrOpenArchive()
	now := time.Now()
	for i := 1; i <= 5; i++ {
		if err := a.Append(int64(i), providers.Message{Role: "user", Content: "m"}, now.AddDate(0, 0, -100)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := a.AppendSummary(memory.SummaryRecord{GeneratedAt: now.AddDate(0, 0, -100), Summary: "s"}); err != nil {
			t.Fatalf("AppendSummary %d: %v", i, err)
		}
	}

	mgr.pruneArchive(a)

	_, maxSeq, err := a.Bounds()
	if err != nil {
		t.Fatalf("Bounds: %v", err)
	}
	if maxSeq != 5 {
		t.Fatalf("maxSeq = %d, want 5 (no prune)", maxSeq)
	}
	metas, err := a.ListSummaries()
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if len(metas) != 3 {
		t.Fatalf("summaries = %d, want 3 (no prune)", len(metas))
	}
}
