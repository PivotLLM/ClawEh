// ClawEh
// License: MIT

package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T, opts ...Option) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "sub", FileName), opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func flush(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func TestOpenCreatesPrivateFile(t *testing.T) {
	s := openTestStore(t)
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 0600", got)
	}
	di, err := os.Stat(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("dir mode = %o, want 0700", got)
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestRecordAndQueryFilters(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{TS: base, Kind: KindToolCall, Agent: "alice", Session: "s1", Channel: "slack", Sender: "u1", Tool: "file_read", Outcome: OutcomeOK, DurationMS: 12, TurnID: "aaaa1111"},
		{TS: base.Add(time.Minute), Kind: KindToolCall, Agent: "bob", Session: "s2", Tool: "exec", Outcome: OutcomeError, DurationMS: 5},
		{TS: base.Add(2 * time.Minute), Kind: KindConfigWrite, Actor: "eric", Sender: "127.0.0.1", Summary: "providers", Details: `{"keys":["providers"]}`, Outcome: OutcomeOK},
		{TS: base.Add(3 * time.Minute), Kind: KindAuth, Actor: "eric", Sender: "127.0.0.1", Summary: "login", Outcome: OutcomeOK},
	}
	for _, e := range events {
		s.Record(e)
	}
	flush(t, s)

	ctx := context.Background()
	all, err := s.Query(ctx, Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != len(events) {
		t.Fatalf("rows = %d, want %d", len(all), len(events))
	}
	// Newest first, ids ascending in insertion order.
	if all[0].Kind != KindAuth || all[3].Kind != KindToolCall {
		t.Errorf("order wrong: %v", all)
	}
	if all[3].ID >= all[0].ID {
		t.Errorf("ids not ascending with insertion: first=%d last=%d", all[3].ID, all[0].ID)
	}
	got := all[3]
	if got.Agent != "alice" || got.Session != "s1" || got.Channel != "slack" || got.Sender != "u1" ||
		got.Tool != "file_read" || got.Outcome != OutcomeOK || got.DurationMS != 12 || got.TurnID != "aaaa1111" ||
		!got.TS.Equal(base) {
		t.Errorf("round trip mismatch: %+v", got)
	}

	tests := []struct {
		name string
		f    Filter
		want int
	}{
		{"kind", Filter{Kind: KindToolCall}, 2},
		{"agent", Filter{Agent: "bob"}, 1},
		{"session", Filter{Session: "s1"}, 1},
		{"since", Filter{Since: base.Add(90 * time.Second)}, 2},
		{"until", Filter{Until: base.Add(90 * time.Second)}, 2},
		{"since+until", Filter{Since: base.Add(30 * time.Second), Until: base.Add(150 * time.Second)}, 2},
		{"limit", Filter{Limit: 1}, 1},
		{"before_id", Filter{BeforeID: all[1].ID}, 2},
		{"none", Filter{Agent: "nobody"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := s.Query(ctx, tc.f)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(rows) != tc.want {
				t.Errorf("rows = %d, want %d", len(rows), tc.want)
			}
		})
	}
}

func TestQueryPaginationByBeforeID(t *testing.T) {
	s := openTestStore(t)
	for i := range 7 {
		s.Record(Event{Kind: KindToolCall, Tool: "t", DurationMS: int64(i)})
	}
	flush(t, s)
	ctx := context.Background()

	var seen []int64
	var before int64
	for {
		rows, err := s.Query(ctx, Filter{Limit: 3, BeforeID: before})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			seen = append(seen, r.ID)
		}
		before = rows[len(rows)-1].ID
	}
	if len(seen) != 7 {
		t.Fatalf("paged through %d rows, want 7: %v", len(seen), seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] >= seen[i-1] {
			t.Fatalf("ids not strictly descending: %v", seen)
		}
	}
}

func TestQueryLimitClamp(t *testing.T) {
	s := openTestStore(t)
	for range 3 {
		s.Record(Event{Kind: KindAuth})
	}
	flush(t, s)
	rows, err := s.Query(context.Background(), Filter{Limit: MaxQueryLimit + 500})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("rows = %d, want 3", len(rows))
	}
}

func TestDetailsTruncated(t *testing.T) {
	s := openTestStore(t)
	long := strings.Repeat("é", MaxDetailsBytes) // 2 bytes per rune: well past the cap
	s.Record(Event{Kind: KindToolCall, Details: long})
	flush(t, s)
	rows, err := s.Query(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	d := rows[0].Details
	if len(d) > MaxDetailsBytes {
		t.Errorf("details = %d bytes, want <= %d", len(d), MaxDetailsBytes)
	}
	if !strings.HasSuffix(d, "…(truncated)") {
		t.Errorf("details missing truncation marker: %q", d[len(d)-20:])
	}
	if strings.ContainsRune(d, '�') {
		t.Error("truncation split a rune")
	}
}

func TestPrune(t *testing.T) {
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	s := openTestStore(t, withClock(func() time.Time { return now }))
	s.Record(Event{TS: now.Add(-100 * 24 * time.Hour), Kind: KindAuth, Summary: "old"})
	s.Record(Event{TS: now.Add(-89 * 24 * time.Hour), Kind: KindAuth, Summary: "kept"})
	s.Record(Event{Kind: KindAuth, Summary: "now"})
	flush(t, s)

	n, err := s.Prune(context.Background(), now.Add(-RetentionDays*24*time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// The store's startup pruner runs concurrently and may already have taken
	// the old row, so this call removes one row or none; never more.
	if n > 1 {
		t.Errorf("pruned %d, want at most 1", n)
	}
	rows, err := s.Query(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows after prune = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Summary == "old" {
			t.Error("old row survived the prune")
		}
	}
}

func TestPruneRunsAtOpen(t *testing.T) {
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), FileName)
	s, err := Open(context.Background(), path, withClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Record(Event{TS: now.Add(-365 * 24 * time.Hour), Kind: KindAuth})
	flush(t, s)
	if err = s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening runs the startup prune; the year-old row must be gone once the
	// pruner has had its turn.
	s2, err := Open(context.Background(), path, withClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if closeErr := s2.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, err := s2.Query(context.Background(), Filter{})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(rows) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("startup prune did not remove the expired row: %d left", len(rows))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRecordDropsWhenFull: a full queue never blocks the caller; the overflow
// is counted and the rest is still written.
func TestRecordDropsWhenFull(t *testing.T) {
	s := openTestStore(t, WithBufferSize(4))
	// Hold the writer: the first batch it takes blocks on a write lock held by
	// an open transaction, so everything after it queues up.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err = tx.Exec(`INSERT INTO events (ts, kind) VALUES (0, 'lock')`); err != nil {
		t.Fatalf("lock: %v", err)
	}

	const total = 50
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range total {
			s.Record(Event{Kind: KindToolCall, DurationMS: int64(i)})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked with a full queue")
	}
	if err = tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	flush(t, s)

	dropped := s.Dropped()
	if dropped == 0 {
		t.Fatal("expected drops with a 4-slot queue and 50 events")
	}
	rows, err := s.Query(context.Background(), Filter{Kind: KindToolCall, Limit: MaxQueryLimit})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if uint64(len(rows))+dropped != total {
		t.Errorf("written %d + dropped %d != %d", len(rows), dropped, total)
	}
}

func TestRecordConcurrent(t *testing.T) {
	s := openTestStore(t)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 25 {
				s.Record(Event{Kind: KindToolCall, Agent: "g", DurationMS: int64(g*100 + i)})
			}
		})
	}
	wg.Wait()
	flush(t, s)
	rows, err := s.Query(context.Background(), Filter{Limit: MaxQueryLimit})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 200 {
		t.Errorf("rows = %d, want 200 (dropped=%d)", len(rows), s.Dropped())
	}
}

func TestCloseDrainsQueueAndRejectsLater(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for range 10 {
		s.Record(Event{Kind: KindAuth})
	}
	if err = s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s.Record(Event{Kind: KindAuth}) // must not panic on the closed queue
	if _, err = s.Query(context.Background(), Filter{}); err == nil {
		t.Error("Query on a closed store should fail")
	}
	if err = s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	s2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() {
		if closeErr := s2.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	}()
	rows, err := s2.Query(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 10 {
		t.Errorf("rows after close/reopen = %d, want 10", len(rows))
	}
}

func TestNilStoreIsNoOp(t *testing.T) {
	var s *Store
	s.Record(Event{Kind: KindAuth})
	if s.Dropped() != 0 || s.Path() != "" {
		t.Error("nil store should report nothing")
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Errorf("Flush: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := s.Query(context.Background(), Filter{}); err == nil {
		t.Error("Query on nil store should fail")
	}
}

func TestDefaultInitAuthClose(t *testing.T) {
	if Default() != nil {
		t.Fatal("Default should be nil before Init")
	}
	Auth("login", "nobody", "127.0.0.1", OutcomeOK) // no store: silently dropped

	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		if err := Close(); err != nil { // idempotent: nil after the Close below
			t.Errorf("Close: %v", err)
		}
	})
	if Default().Path() != filepath.Join(dir, FileName) {
		t.Errorf("Path = %q", Default().Path())
	}

	Auth("login", "eric", "10.0.0.1", OutcomeError)
	flush(t, Default())
	rows, err := Default().Query(context.Background(), Filter{Kind: KindAuth})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.Actor != "eric" || got.Sender != "10.0.0.1" || got.Summary != "login" || got.Outcome != OutcomeError {
		t.Errorf("auth row = %+v", got)
	}

	if err := Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if Default() != nil {
		t.Error("Default should be nil after Close")
	}
	if err := Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestActorContext(t *testing.T) {
	if got := ActorFromContext(context.Background()); got != "" {
		t.Errorf("empty ctx actor = %q", got)
	}
	ctx := WithActor(context.Background(), "eric")
	if got := ActorFromContext(ctx); got != "eric" {
		t.Errorf("actor = %q, want eric", got)
	}
	// The exported key is the contract the auth middleware uses directly.
	ctx = context.WithValue(context.Background(), ActorKey, "bob")
	if got := ActorFromContext(ctx); got != "bob" {
		t.Errorf("actor via ActorKey = %q, want bob", got)
	}
}
