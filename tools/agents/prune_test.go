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
	oldSub := write("agent_penny_subagent_abc.cogmem.db")
	oldSubArch := write("agent_penny_subagent_abc.archive.db")
	recentSub := write("agent_penny_subagent_def.cogmem.db")
	mainSession := write("agent_penny_main.cogmem.db") // must never be touched

	now := time.Now()
	// Age the two "old" sub-agent files past 24h; leave the recent one fresh.
	old := now.Add(-48 * time.Hour)
	for _, p := range []string{oldSub, oldSubArch} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	removed := PruneOrphanSubagentSessions(ws, 24*time.Hour, now)
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
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
