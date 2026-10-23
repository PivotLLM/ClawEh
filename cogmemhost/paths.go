// ClawEh - Cognitive Memory
// License: MIT

package cogmemhost

import (
	"fmt"
	"path/filepath"

	"github.com/PivotLLM/cogmem/store"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/alerts"
	"github.com/PivotLLM/ClawEh/config"
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

// StoreOptions are the store options that give a memory's directory and files
// modes; every opener of an agent's store passes them, since opening sets the
// database files to the configured mode.
func StoreOptions(modes config.Modes) []store.Option {
	return []store.Option{store.WithFolderPermissions(modes.Dir), store.WithFilePermissions(modes.File)}
}

// Migrate opens the agent's memory once at load so any pending schema
// migration runs now rather than mid-conversation, and logs what changed.
// The store is opened with modes (see StoreOptions).
// The layout itself is not migrated here: moving a pre-0.5.2 per-session file
// into the cogmem directory is a one-time operator step (see the changelog).
func Migrate(agentID, workspace string, modes config.Modes) {
	dir := Dir(workspace)
	if dir == "" {
		return
	}
	r := store.Migrate(dir, StoreOptions(modes)...)
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
