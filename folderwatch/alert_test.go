// ClawEh
// License: MIT

package folderwatch

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
)

type alertRecorder struct {
	mu     sync.Mutex
	alerts []alerter.Alert
}

func (r *alertRecorder) Send(a alerter.Alert) {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
}
func (r *alertRecorder) Normal(string, string, ...string)    {}
func (r *alertRecorder) Urgent(string, string, ...string)    {}
func (r *alertRecorder) Emergency(string, string, ...string) {}
func (r *alertRecorder) Close(context.Context) error         { return nil }

// TestDetectNewFiles_AlertsWhenMarkerNotSaved: when the agent's own state
// cannot hold the marker, one alert keyed by the agent and the folder is
// raised, naming both; a writable state raises none.
func TestDetectNewFiles_AlertsWhenMarkerNotSaved(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, permission denial tests don't apply")
	}
	rec := &alertRecorder{}
	w := New(func() *config.Config { return nil }, nil, time.Hour, rec, nil)
	folder := t.TempDir()

	ok := t.TempDir()
	w.detectNewFiles("alice", "Alice", folder, markerPath(ok, "alice", folder), false)
	if len(rec.alerts) != 0 {
		t.Fatalf("a writable state must not alert, got %+v", rec.alerts)
	}

	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(ro, 0o700); err != nil {
			t.Error(err)
		}
	})
	// No list saved yet: every scan starts over, so no notice is ever sent,
	// not even for a file added later, and the alert says so.
	w.detectNewFiles("alice", "Alice", folder, markerPath(ro, "alice", folder), false)
	if err := os.WriteFile(filepath.Join(folder, "new.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := w.detectNewFiles("alice", "Alice", folder, markerPath(ro, "alice", folder), false); len(got) != 0 {
		t.Fatalf("without a saved list nothing can be new, got %v", got)
	}
	want := "New-file notices for " + folder + " (Alice) are not sent: the list of files already there could not be saved"
	if len(rec.alerts) != 2 || rec.alerts[0].Priority != alerter.Normal || rec.alerts[0].EventID != "allowed-folder:alice:"+folder ||
		rec.alerts[0].Title != "Allowed folder file list not saved" || rec.alerts[0].Description != want {
		t.Fatalf("a baseline save failure: got %+v, want %q", rec.alerts, want)
	}
}

// A list that was saved and then cannot be updated: the new file is still
// reported, and the alert says the same files may be reported again.
func TestDetectNewFiles_AlertsWhenMarkerUpdateFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, permission denial tests don't apply")
	}
	rec := &alertRecorder{}
	w := New(func() *config.Config { return nil }, nil, time.Hour, rec, nil)
	folder, ws := t.TempDir(), t.TempDir()
	marker := markerPath(ws, "alice", folder)
	w.detectNewFiles("alice", "Alice", folder, marker, false)
	dir := filepath.Dir(marker)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	file := filepath.Join(folder, "new.md")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := w.detectNewFiles("alice", "Alice", folder, marker, false); len(got) != 1 || got[0] != file {
		t.Fatalf("want [%s], got %v", file, got)
	}
	want := "Alice's allowed folder " + folder + ": the same files may be reported again"
	if len(rec.alerts) != 1 || rec.alerts[0].Description != want {
		t.Fatalf("an update failure: got %+v, want %q", rec.alerts, want)
	}
}
