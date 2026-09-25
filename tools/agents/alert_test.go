// ClawEh
// License: MIT

package agents

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tenebris-tech/alerter"
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

// TestPersist_AlertsWhenUnwritable: a status or results file that cannot be
// written raises one low alert each, keyed by the store; a writable workspace
// raises none.
func TestPersist_AlertsWhenUnwritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, permission denial tests don't apply")
	}
	rec := &alertRecorder{}
	ok := NewSubagentManager(SubagentManagerConfig{Workspace: t.TempDir(), Live: NewLiveSet(), Alerter: rec})
	ok.persistStatus(ok.tasksDir(), &TaskRecord{UUID: "u1"})
	ok.persistResults(ok.tasksDir(), &TaskResults{UUID: "u1"})
	if len(rec.alerts) != 0 {
		t.Fatalf("writable workspace must not alert, got %+v", rec.alerts)
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
	bad := NewSubagentManager(SubagentManagerConfig{Workspace: ro, Live: NewLiveSet(), Alerter: rec})
	bad.persistStatus(bad.tasksDir(), &TaskRecord{UUID: "u2"})
	bad.persistResults(bad.tasksDir(), &TaskResults{UUID: "u2"})
	if len(rec.alerts) != 2 {
		t.Fatalf("unwritable workspace must alert once per file, got %+v", rec.alerts)
	}
	for _, a := range rec.alerts {
		if a.Priority != alerter.Normal || a.EventID != "subagent-store" || a.Title != "Sub-agent record not written" ||
			a.Description != "task u2: its status/results file could not be written, so it cannot be resumed or reported" {
			t.Fatalf("want low alert keyed by the store naming the task, got %+v", a)
		}
	}
}
