// ClawEh
// License: MIT

package cron

import (
	"context"
	"errors"
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
func (r *alertRecorder) High(string, string, ...string) {}
func (r *alertRecorder) Low(string, string, ...string)  {}
func (r *alertRecorder) Close(context.Context) error    { return nil }

// TestExecuteJobByID_AlertsOnFailure: a failing handler raises one low alert
// keyed by the job id; a successful one raises none.
func TestExecuteJobByID_AlertsOnFailure(t *testing.T) {
	fail := false
	cs := newTestServiceWithHandler(t, func(*CronJob) (string, error) {
		if fail {
			return "", errors.New("handler failure")
		}
		return "ok", nil
	})
	rec := &alertRecorder{}
	cs.SetAlerter(rec)
	job := addEnabledJob(t, cs, "every", new(int64(60_000)))

	cs.executeJobByID(job.ID)
	if len(rec.alerts) != 0 {
		t.Fatalf("success must not alert, got %+v", rec.alerts)
	}
	fail = true
	cs.executeJobByID(job.ID)
	if len(rec.alerts) != 1 || rec.alerts[0].High || rec.alerts[0].EventID != job.ID ||
		rec.alerts[0].Title != "Scheduled job failed" || rec.alerts[0].Details != "handler failure" {
		t.Fatalf("failure must alert low once for the job, got %+v", rec.alerts)
	}
}

// TestCronStore_LoadErrorAndSaveAlert: a corrupt store is reported through
// LoadError (the gateway alerts on it), and a store that cannot be written
// raises one low alert keyed "cron-store".
func TestCronStore_LoadErrorAndSaveAlert(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "jobs.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cs := NewCronService(corrupt, nil); cs.LoadError() == nil {
		t.Fatal("corrupt store must surface through LoadError")
	}

	// A job runs and its state cannot be saved because the store path has
	// become unwritable (its parent is now a file).
	cs := newTestServiceWithHandler(t, func(*CronJob) (string, error) { return "ok", nil })
	rec := &alertRecorder{}
	cs.SetAlerter(rec)
	job := addEnabledJob(t, cs, "every", new(int64(60_000)))
	cs.storePath = filepath.Join(corrupt, "jobs.json")
	cs.executeJobByID(job.ID)
	if len(rec.alerts) != 1 || rec.alerts[0].High || rec.alerts[0].EventID != "cron-store" ||
		rec.alerts[0].Title != "Cron store not saved" {
		t.Fatalf("save failure must alert low once, got %+v", rec.alerts)
	}
}
