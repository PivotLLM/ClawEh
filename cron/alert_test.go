// ClawEh
// License: MIT

package cron

import (
	"context"
	"errors"
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
