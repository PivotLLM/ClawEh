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
	w := New(func() *config.Config { return nil }, nil, time.Hour, rec)
	folder := t.TempDir()

	ok := t.TempDir()
	w.detectNewFiles("alice", "Alice", folder, markerPath(ok, "alice", folder))
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
	w.detectNewFiles("alice", "Alice", folder, markerPath(ro, "alice", folder))
	if len(rec.alerts) != 1 || rec.alerts[0].Priority != alerter.Normal || rec.alerts[0].EventID != "allowed-folder:alice:"+folder ||
		rec.alerts[0].Title != "Allowed folder file list not saved" ||
		rec.alerts[0].Description != "Alice's allowed folder "+folder+": the same files may be reported again" {
		t.Fatalf("a marker save failure must alert once for the agent and folder, got %+v", rec.alerts)
	}
}
