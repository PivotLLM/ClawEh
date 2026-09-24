// ClawEh
// License: MIT

package mountwatch

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
func (r *alertRecorder) High(string, string, ...string) {}
func (r *alertRecorder) Low(string, string, ...string)  {}
func (r *alertRecorder) Close(context.Context) error    { return nil }

// TestDetectNewFiles_AlertsWhenMarkerNotWritten: a mount whose marker cannot
// be written raises one low alert keyed by the mount path; a writable mount
// raises none.
func TestDetectNewFiles_AlertsWhenMarkerNotWritten(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, permission denial tests don't apply")
	}
	rec := &alertRecorder{}
	w := New(func() *config.Config { return nil }, nil, time.Hour, rec)

	ok := t.TempDir()
	w.detectNewFiles("notes", ok)
	if len(rec.alerts) != 0 {
		t.Fatalf("writable mount must not alert, got %+v", rec.alerts)
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
	w.detectNewFiles("notes", ro)
	if len(rec.alerts) != 1 || rec.alerts[0].High || rec.alerts[0].EventID != "mount:"+ro ||
		rec.alerts[0].Title != "Mount marker not written" {
		t.Fatalf("marker write failure must alert low once for the mount, got %+v", rec.alerts)
	}
}
