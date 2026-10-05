// ClawEh
// License: MIT

package agents

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Task status, results and run-marker files live under the agent workspace and
// must be owner-only, like everything else ClawEh creates under CLAW_HOME.
func TestTaskFiles_OwnerOnlyModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}
	dir := filepath.Join(t.TempDir(), "tasks")
	const id = "task-1"

	if err := writeStatus(dir, &TaskRecord{UUID: id}); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}
	if err := writeResults(dir, &TaskResults{UUID: id}); err != nil {
		t.Fatalf("writeResults: %v", err)
	}
	if err := markRun(dir, id); err != nil {
		t.Fatalf("markRun: %v", err)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat tasks dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("tasks dir mode = %04o, want 0700", got)
	}
	for _, p := range []string{statusPath(dir, id), resultsPath(dir, id), runPath(dir, id)} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", filepath.Base(p), got)
		}
	}
}
