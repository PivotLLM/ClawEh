// ClawEh - sub-agent session cleanup
// License: MIT

package agents

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// subagentSessionMarker identifies a sub-agent session's files in a workspace's
// sessions dir. Sub-agent session keys are "agent:<id>:subagent:<uuid>", which
// SanitizeSessionKey turns into "agent_<id>_subagent_<uuid>".
const subagentSessionMarker = "_subagent_"

// PruneOrphanSubagentSessions deletes leftover sub-agent session DB files
// (cogmem snapshot + conversation archive, with their -wal/-shm) under
// <workspace>/sessions whose mtime is older than olderThan. Sub-agent sessions
// are cleaned up immediately on normal completion; this reclaims files left by a
// crash mid-run, after a grace window so their artefacts can be inspected first.
// Returns the number of files removed. Intended to run once at startup.
func PruneOrphanSubagentSessions(workspace string, olderThan time.Duration, now time.Time) int {
	dir := filepath.Join(workspace, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0 // no sessions dir (or unreadable) → nothing to prune
	}
	cutoff := now.Add(-olderThan)
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.Contains(e.Name(), subagentSessionMarker) {
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
	if removed > 0 {
		logger.InfoCF("agent", "pruned orphan sub-agent session files", map[string]any{
			"workspace": workspace, "removed": removed,
		})
	}
	return removed
}
