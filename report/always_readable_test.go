// ClawEh
// License: MIT

package report

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// Every always-readable workspace folder has a note in the Folder access table.
func TestAlwaysReadableNotesCoverDirs(t *testing.T) {
	for _, dir := range config.AlwaysReadableWorkspaceDirs {
		if alwaysReadableNotes[dir] == "" {
			t.Errorf("no note for always-readable folder %q", dir)
		}
	}
}
