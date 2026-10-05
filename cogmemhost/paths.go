// ClawEh - Cognitive Memory
// License: MIT

package cogmemhost

import (
	"fmt"
	"path/filepath"

	"github.com/PivotLLM/cogmem/store"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/alerts"
	"github.com/PivotLLM/ClawEh/logger"
)

// DirName is the directory inside an agent's state directory that cogmem owns.
const DirName = "cogmem"

// Dir returns the agent's memory directory, <state dir>/cogmem (the state
// directory is the workspace for a config agent). One agent, one memory: every
// session of the agent shares it. Sessions, archives and the rest of the
// workspace can be deleted and the memory survives, and the directory can be
// backed up or copied to a new agent as a unit.
func Dir(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, DirName)
}

// Migrate opens the agent's memory once at load so any pending schema
// migration runs now rather than mid-conversation, and logs what changed.
// The layout itself is not migrated here: moving a pre-0.5.2 per-session file
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
		alerts.Send(alerter.Alert{
			Title:       "Cognitive memory migration failed",
			Description: "agent " + agentID + ": " + r.Path + " was not migrated, so this agent's memory is unavailable or stale",
			Details:     r.Err.Error(),
			EventID:     "cogmem:" + agentID,
		})
	case r.Migrated():
		logger.InfoCF("cogmem", "Migrated cognitive-memory database", map[string]any{
			"agent": agentID, "path": r.Path, "from": r.From, "to": r.To,
			"snapshot": fmt.Sprintf("%s.pre-v%d.db", r.Path, r.From),
		})
	}
}
