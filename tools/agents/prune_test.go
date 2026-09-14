package agents

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneOrphanSubagentSessions(t *testing.T) {
	ws := t.TempDir()
	sessions := filepath.Join(ws, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string) string {
		p := filepath.Join(sessions, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldSub := write("agent_penny_subagent_abc.archive.db")
	oldSubArch := write("agent_penny_subagent_abc.archive.db-wal")
	recentSub := write("agent_penny_subagent_def.archive.db")
	mainSession := write("agent_penny_main.archive.db")      // must never be touched
	legacyMem := write("agent_penny_subagent_abc.cogmem.db") // not ours: memory never lived here after 0.5.2

	now := time.Now()
	// Age the two "old" sub-agent files past 24h; leave the recent one fresh.
	old := now.Add(-48 * time.Hour)
	for _, p := range []string{oldSub, oldSubArch} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	// A stale memory snapshot directory and a fresh one.
	oldSnap := filepath.Join(ws, "cogmem", "subagents", "agent_penny_subagent_abc")
	freshSnap := filepath.Join(ws, "cogmem", "subagents", "agent_penny_subagent_def")
	for _, d := range []string{oldSnap, freshSnap} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "cogmem.db"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(oldSnap, old, old); err != nil {
		t.Fatal(err)
	}

	removed := PruneOrphanSubagentSessions(ws, 24*time.Hour, now)
	if removed != 3 {
		t.Fatalf("removed = %d, want 3 (two files and one snapshot directory)", removed)
	}
	if _, err := os.Stat(oldSnap); err == nil {
		t.Fatal("stale snapshot directory not removed")
	}
	if _, err := os.Stat(legacyMem); err != nil {
		t.Fatal("prune must only touch the files a sub-agent session writes today")
	}
	if _, err := os.Stat(freshSnap); err != nil {
		t.Fatal("fresh snapshot directory must survive")
	}
	for _, p := range []string{oldSub, oldSubArch} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s pruned", filepath.Base(p))
		}
	}
	// Recent sub-agent file and the main session must remain.
	if _, err := os.Stat(recentSub); err != nil {
		t.Error("recent sub-agent file (<24h) must be kept")
	}
	if _, err := os.Stat(mainSession); err != nil {
		t.Error("main session file must never be pruned")
	}
}
