// ClawEh
// License: MIT

package alerts

import (
	"context"
	"sync"
	"testing"

	"github.com/tenebris-tech/alerter"
)

type recorder struct {
	mu     sync.Mutex
	alerts []alerter.Alert
}

func (r *recorder) Send(a alerter.Alert) {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
}
func (r *recorder) Normal(string, string, ...string)    {}
func (r *recorder) Urgent(string, string, ...string)    {}
func (r *recorder) Emergency(string, string, ...string) {}
func (r *recorder) Close(context.Context) error         { return nil }

// TestSetAndSend: the default is a no-op, Set installs the alerter Send uses,
// and Set(nil) restores the no-op.
func TestSetAndSend(t *testing.T) {
	t.Cleanup(func() { Set(nil) })
	if _, ok := Default().(alerter.Nop); !ok {
		t.Fatalf("default must be Nop, got %T", Default())
	}
	Send(alerter.Alert{Title: "dropped"}) // no-op, must not panic

	rec := &recorder{}
	Set(rec)
	Send(alerter.Alert{Title: "kept", EventID: "x"})
	if len(rec.alerts) != 1 || rec.alerts[0].Title != "kept" {
		t.Fatalf("got %+v", rec.alerts)
	}

	Set(nil)
	if _, ok := Default().(alerter.Nop); !ok {
		t.Fatalf("Set(nil) must restore Nop, got %T", Default())
	}
}
