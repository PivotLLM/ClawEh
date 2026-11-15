// ClawEh
// License: MIT

package cron

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/internal/clock"
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
	if len(rec.alerts) != 1 || rec.alerts[0].Priority != alerter.Normal || rec.alerts[0].EventID != job.ID ||
		rec.alerts[0].Title != "Scheduled job failed" || rec.alerts[0].Details != "handler failure" {
		t.Fatalf("failure must alert low once for the job, got %+v", rec.alerts)
	}
}

// TestExecuteJobByID_FailingJobAlertsOnceThenDaily: a job whose every run
// fails alerts on the first, then once a day with the count of failed runs
// held back; a run that succeeds ends that, so the next failure alerts at
// once.
func TestExecuteJobByID_FailingJobAlertsOnceThenDaily(t *testing.T) {
	fail := true
	cs := newTestServiceWithHandler(t, func(*CronJob) (string, error) {
		if fail {
			return "", errors.New("handler failure")
		}
		return "ok", nil
	})
	fc := clock.NewFake(time.Date(2026, time.November, 14, 9, 2, 0, 0, time.UTC))
	cs.clock = fc
	rec := &alertRecorder{}
	cs.SetAlerter(rec)
	job := addEnabledJob(t, cs, "every", new(int64(3_600_000)))

	for range 24 { // hourly, for a day
		cs.executeJobByID(job.ID)
		fc.Advance(time.Hour)
	}
	if len(rec.alerts) != 1 || strings.Contains(rec.alerts[0].Description, "ongoing") {
		t.Fatalf("a day of failed hourly runs must raise one alert, got %+v", rec.alerts)
	}

	cs.executeJobByID(job.ID)
	if len(rec.alerts) != 2 || rec.alerts[1].EventID != job.ID ||
		!strings.HasSuffix(rec.alerts[1].Description, "; ongoing since Nov 14 09:02, 23 more since the last alert") {
		t.Fatalf("a day later the job must alert once more with the count, got %+v", rec.alerts)
	}

	fail = false
	cs.executeJobByID(job.ID)
	fail = true
	cs.executeJobByID(job.ID)
	if len(rec.alerts) != 3 || strings.Contains(rec.alerts[2].Description, "ongoing") {
		t.Fatalf("a failure after a successful run must alert as new, got %+v", rec.alerts)
	}
}

// TestRemoveJob_EndsItsFailureAlert: a removed job leaves nothing behind, so
// a job that later gets the same id alerts on its first failure.
func TestRemoveJob_EndsItsFailureAlert(t *testing.T) {
	cs := newTestServiceWithHandler(t, func(*CronJob) (string, error) {
		return "", errors.New("handler failure")
	})
	rec := &alertRecorder{}
	cs.SetAlerter(rec)
	job := addEnabledJob(t, cs, "every", new(int64(60_000)))

	cs.executeJobByID(job.ID)
	if removed, err := cs.RemoveJob(job.ID); err != nil || !removed {
		t.Fatalf("RemoveJob = %v, %v", removed, err)
	}
	if o := cs.jobFaults.Report(job.ID, time.Now()); !o.Alert || o.Ongoing {
		t.Fatalf("a removed job's failure state must be gone, got %+v", o)
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
	if len(rec.alerts) != 1 || rec.alerts[0].Priority != alerter.Normal || rec.alerts[0].EventID != "cron-store" ||
		rec.alerts[0].Title != "Cron store not saved" {
		t.Fatalf("save failure must alert low once, got %+v", rec.alerts)
	}
}
