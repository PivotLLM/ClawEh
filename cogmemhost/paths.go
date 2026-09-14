// ClawEh - Cognitive Memory
// License: MIT

package cogmemhost

import (
	"fmt"
	"path/filepath"

	"github.com/PivotLLM/cogmem/store"
	"github.com/PivotLLM/ctxengine/memory"

	"github.com/PivotLLM/ClawEh/logger"
)

// DirName is the directory inside an agent workspace that cogmem owns.
const DirName = "cogmem"

// Dir returns the agent's memory directory, <workspace>/cogmem. One agent, one
// memory: every session of the agent shares it. Sessions, archives and the
// rest of the workspace can be deleted and the memory survives, and the
// directory can be backed up or copied to a new agent as a unit.
func Dir(workspace string) string {
	if workspace == "" {
		return ""
	}
	return filepath.Join(workspace, DirName)
}

// SubagentDir returns the throwaway directory a sub-agent's snapshot of the
// memory lives in for the length of its run: <workspace>/cogmem/subagents/<key>.
func SubagentDir(workspace, sessionKey string) string {
	return filepath.Join(Dir(workspace), "subagents", memory.SanitizeSessionKey(sessionKey))
}

// Migrate opens the agent's memory once at load so any pending schema
// migration runs now rather than mid-conversation, and logs what changed.
// The layout itself is not migrated here: moving a pre-0.5.1 per-session file
// into the cogmem directory is a one-time operator step (see the changelog).
func Migrate(agentID, workspace string) {
	dir := Dir(workspace)
	if dir == "" {
		return
	}
	r := store.Migrate(dir)
	switch {
	case r.Err != nil:
		logger.ErrorCF("cogmem", "Failed to migrate cognitive-memory database",
			map[string]any{"agent": agentID, "path": r.Path, "error": r.Err.Error()})
	case r.Migrated():
		logger.InfoCF("cogmem", "Migrated cognitive-memory database", map[string]any{
			"agent": agentID, "path": r.Path, "from": r.From, "to": r.To,
			"snapshot": fmt.Sprintf("%s.pre-v%d.db", r.Path, r.From),
		})
	}
}
