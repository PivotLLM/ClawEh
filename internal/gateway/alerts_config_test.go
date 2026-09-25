// ClawEh
// License: MIT

package gateway

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
func (r *alertRecorder) Normal(string, string, ...string)    {}
func (r *alertRecorder) Urgent(string, string, ...string)    {}
func (r *alertRecorder) Emergency(string, string, ...string) {}
func (r *alertRecorder) Close(context.Context) error         { return nil }

// TestAlertConfigFileInvalid: an unapplied config edit alerts high under the
// "config" id, so it collapses with a reload failure for the same file.
func TestAlertConfigFileInvalid(t *testing.T) {
	rec := &alertRecorder{}
	alertConfigFileInvalid(rec, "/tmp/config.json", errors.New("bad json"))
	if len(rec.alerts) != 1 || rec.alerts[0].Priority != alerter.Normal || rec.alerts[0].EventID != "config" ||
		rec.alerts[0].Title != "Config file invalid" || rec.alerts[0].Details != "bad json" {
		t.Fatalf("got %+v", rec.alerts)
	}
}
