// ClawEh
// License: MIT

package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/PivotLLM/ctxengine/memory"
	"github.com/PivotLLM/ctxengine/session"

	"github.com/PivotLLM/ClawEh/providers"
)

// seedRetentionArchive writes one message to key's archive through the real
// store, then rewrites the state row so the session was last updated at
// updated (the zero time leaves it unset, which retention reads as "use the
// file mtime") and carries the pending-turn flag.
func seedRetentionArchive(t *testing.T, dir, key string, updated time.Time, pending bool) string {
	t.Helper()
	store, err := session.NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	addFullMessage(t, store, key, providers.Message{Role: "user", Content: "hello"})
	closeT(t, store)
	path := memory.ArchivePath(dir, key)
	a, err := memory.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.State()
	if err != nil {
		t.Fatal(err)
	}
	st.UpdatedAt = updated
	st.PendingTurn = pending
	if err := a.SetState(st); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func archiveExists(t *testing.T, dir, key string) bool {
	t.Helper()
	_, err := os.Stat(memory.ArchivePath(dir, key))
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return false
}

// TestPruneSessions deletes only sessions that are old, closed and idle: a
// recent session, a pending turn, an open (cached) session, the main and
// service sessions and an unreadable file all stay.
func TestPruneSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)

	const (
		oldClosed  = "agent:alice:telegram:direct:111"
		oldGroup   = "agent:alice:slack:group:c1/1234.5"
		oldPending = "agent:alice:telegram:direct:222"
		oldOpen    = "agent:alice:telegram:direct:333"
		recent     = "agent:alice:telegram:direct:444"
		mainKey    = "agent:alice:main"
		serviceKey = "agent:alice:service"
	)
	seedRetentionArchive(t, dir, oldClosed, old, false)
	seedRetentionArchive(t, dir, oldGroup, old, false)
	seedRetentionArchive(t, dir, oldPending, old, true)
	seedRetentionArchive(t, dir, oldOpen, old, false)
	seedRetentionArchive(t, dir, recent, now.Add(-2*24*time.Hour), false)
	seedRetentionArchive(t, dir, mainKey, old, false)
	seedRetentionArchive(t, dir, serviceKey, old, false)
	junk := filepath.Join(dir, "not-a-db.archive.db")
	if err := os.WriteFile(junk, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}

	isOpen := func(key string) bool { return key == oldOpen }
	rep := pruneSessions(dir, nil, now, 30, isOpen)

	if len(rep.errors) != 0 {
		t.Fatalf("errors: %v", rep.errors)
	}
	slices.Sort(rep.deleted)
	if want := []string{oldGroup, oldClosed}; !slices.Equal(rep.deleted, want) {
		t.Fatalf("deleted %v, want %v", rep.deleted, want)
	}
	if rep.skippedOpen != 1 {
		t.Fatalf("skippedOpen = %d, want 1", rep.skippedOpen)
	}
	for _, key := range []string{oldClosed, oldGroup} {
		if archiveExists(t, dir, key) {
			t.Errorf("%s still on disk", key)
		}
	}
	for _, key := range []string{oldPending, oldOpen, recent, mainKey, serviceKey} {
		if !archiveExists(t, dir, key) {
			t.Errorf("%s was deleted", key)
		}
	}
	if _, err := os.Stat(junk); err != nil {
		t.Errorf("unreadable file removed: %v", err)
	}
}

// TestPruneSessions_MtimeFallback: a state row without UpdatedAt (a session
// migrated from the JSONL store) is judged by the archive file's mtime.
func TestPruneSessions_MtimeFallback(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	now := time.Now()
	const oldKey, newKey = "agent:alice:telegram:direct:1", "agent:alice:telegram:direct:2"
	oldPath := seedRetentionArchive(t, dir, oldKey, time.Time{}, false)
	seedRetentionArchive(t, dir, newKey, time.Time{}, false)
	stamp := now.Add(-45 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	rep := pruneSessions(dir, nil, now, 30, nil)

	if !slices.Equal(rep.deleted, []string{oldKey}) || len(rep.errors) != 0 {
		t.Fatalf("deleted %v (errors %v), want [%s]", rep.deleted, rep.errors, oldKey)
	}
	if !archiveExists(t, dir, newKey) {
		t.Fatal("fresh archive deleted")
	}
}

// TestPruneSessions_MissingDir: an agent that never had a session has no
// directory, which is not an error.
func TestPruneSessions_MissingDir(t *testing.T) {
	rep := pruneSessions(filepath.Join(t.TempDir(), "none"), nil, time.Now(), 30, nil)
	if len(rep.deleted) != 0 || len(rep.errors) != 0 {
		t.Fatalf("got %+v", rep)
	}
}

func TestRetentionExempt(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"agent:alice:main", true},
		{"agent:alice:service", true},
		{"agent:alice:telegram:direct:1", false},
		{"agent:alice:device:dev-1", false},
		{"agent:alice:subagent:abc", false},
		{"legacy-key", true},
	}
	for _, tc := range tests {
		if got := retentionExempt(tc.key); got != tc.want {
			t.Errorf("retentionExempt(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// TestRunRetentionPass_Disabled: retention_days 0 deletes no session however
// old, while snapshot pruning still runs.
func TestRunRetentionPass_Disabled(t *testing.T) {
	tl := newTestAgentLoop(t)
	ag := tl.al.GetRegistry().GetDefaultAgent()
	dir := filepath.Join(ag.Workspace, "sessions")
	old := time.Now().Add(-400 * 24 * time.Hour)
	const key = "agent:main:telegram:direct:1"
	seedRetentionArchive(t, dir, key, old, false)
	cogmemDir := filepath.Join(ag.Workspace, "cogmem")
	if err := os.MkdirAll(cogmemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(cogmemDir, "cogmem.db.pre-v6.db")
	if err := os.WriteFile(snap, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(snap, old, old); err != nil {
		t.Fatal(err)
	}

	rep := tl.al.runRetentionPass(time.Now())

	if len(rep.deleted) != 0 || !archiveExists(t, dir, key) {
		t.Fatalf("retention_days=0 deleted %v", rep.deleted)
	}
	if rep.snapshots != 1 {
		t.Fatalf("snapshots = %d, want 1", rep.snapshots)
	}
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Fatalf("snapshot still present: %v", err)
	}
}

// TestRunRetentionPass_Enabled deletes the idle session through the loop's
// registry and leaves the one the loop holds open.
func TestRunRetentionPass_Enabled(t *testing.T) {
	tl := newTestAgentLoop(t)
	tl.cfg.Session.RetentionDays = 30
	ag := tl.al.GetRegistry().GetDefaultAgent()
	dir := filepath.Join(ag.Workspace, "sessions")
	old := time.Now().Add(-31 * 24 * time.Hour)
	const idle, open = "agent:main:telegram:direct:1", "agent:main:telegram:direct:2"
	seedRetentionArchive(t, dir, idle, old, false)
	seedRetentionArchive(t, dir, open, old, false)
	makeEntry(tl.al, ag.ID+":"+open, &trackingContextManager{}, time.Now(), 0)

	rep := tl.al.runRetentionPass(time.Now())

	if !slices.Equal(rep.deleted, []string{idle}) || len(rep.errors) != 0 {
		t.Fatalf("deleted %v (errors %v), want [%s]", rep.deleted, rep.errors, idle)
	}
	if rep.skippedOpen != 1 || !archiveExists(t, dir, open) {
		t.Fatalf("open session not kept: skippedOpen=%d", rep.skippedOpen)
	}
}

// TestReleaseSession drops an idle cached context manager so the archive can
// be deleted, and refuses while a turn holds it.
func TestReleaseSession(t *testing.T) {
	tl := newTestAgentLoop(t)
	ag := tl.al.GetRegistry().GetDefaultAgent()
	const idle, busy = "agent:main:telegram:direct:1", "agent:main:telegram:direct:2"
	cm := &trackingContextManager{}
	makeEntry(tl.al, ag.ID+":"+idle, cm, time.Now(), 0)
	makeEntry(tl.al, ag.ID+":"+busy, &trackingContextManager{}, time.Now(), 1)

	if err := tl.al.ReleaseSession(idle); err != nil {
		t.Fatalf("ReleaseSession(idle): %v", err)
	}
	if !cm.closed.Load() || tl.al.sessionOpen(ag.ID, idle) {
		t.Fatal("idle session's context manager not dropped")
	}
	if err := tl.al.ReleaseSession(busy); err == nil {
		t.Fatal("ReleaseSession(busy) succeeded with a turn in flight")
	}
	if !tl.al.sessionOpen(ag.ID, busy) {
		t.Fatal("busy session's context manager dropped")
	}
}
