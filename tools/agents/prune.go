// ClawEh - sub-agent session cleanup
// License: MIT

package agents

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/logger"
)

// subagentSessionMarker identifies a sub-agent session's files in a workspace's
// sessions dir. Sub-agent session keys are "agent:<id>:subagent:<uuid>", which
// SanitizeSessionKey turns into "agent_<id>_subagent_<uuid>".
const subagentSessionMarker = "_subagent_"

// subagentSessionSuffixes are the files a sub-agent session leaves under
// sessions/: its conversation archive and SQLite's sidecars. Memory lives in
// its own snapshot directory (see below), never here.
var subagentSessionSuffixes = []string{".archive.db", ".archive.db-wal", ".archive.db-shm"}

func isSubagentSessionFile(name string) bool {
	if !strings.Contains(name, subagentSessionMarker) {
		return false
	}
	for _, suffix := range subagentSessionSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// PruneOrphanSubagentSessions deletes leftover sub-agent session files: the
// conversation archive (with its -wal/-shm) under <workspace>/sessions, and the
// memory snapshot directory under <workspace>/cogmem/subagents, whose mtime is
// older than olderThan. Sub-agent sessions are cleaned up immediately on
// normal completion; this reclaims what a crash mid-run left behind, after a
// grace window so the artefacts can be inspected first. Returns the number of
// entries removed. Intended to run once at startup.
func PruneOrphanSubagentSessions(workspace string, olderThan time.Duration, now time.Time) int {
	cutoff := now.Add(-olderThan)
	removed := 0
	dir := filepath.Join(workspace, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0 // no sessions dir (or unreadable) → nothing there to prune
	}
	for _, e := range entries {
		if e.IsDir() || !isSubagentSessionFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			removed++
		}
	}
	// Sub-agent memory snapshots live in their own directories under
	// <workspace>/cogmem/subagents; a crashed run leaves one behind.
	snapDir := filepath.Join(workspace, "cogmem", "subagents")
	if snaps, err := os.ReadDir(snapDir); err == nil {
		for _, e := range snaps {
			info, err := e.Info()
			if err != nil || !e.IsDir() || !info.ModTime().Before(cutoff) {
				continue
			}
			if err := os.RemoveAll(filepath.Join(snapDir, e.Name())); err == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		logger.InfoCF("agent", "pruned orphan sub-agent session files", map[string]any{
			"workspace": workspace, "removed": removed,
		})
	}
	return removed
}
