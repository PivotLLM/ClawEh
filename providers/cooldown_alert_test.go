// ClawEh
// License: MIT

package providers

import (
	"context"
	"sync"
	"testing"

	"github.com/tenebris-tech/alerter"
)

// recorder is an Alerter that keeps what it was sent.
type recorder struct {
	mu     sync.Mutex
	alerts []alerter.Alert
}

func (r *recorder) Send(a alerter.Alert) {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
}

func (r *recorder) Normal(title, desc string, details ...string) {
	r.Send(alerter.Alert{Priority: alerter.Normal, Title: title, Description: desc})
}

func (r *recorder) Urgent(title, desc string, details ...string) {
	r.Send(alerter.Alert{Priority: alerter.Urgent, Title: title, Description: desc})
}

func (r *recorder) Emergency(title, desc string, details ...string) {
	r.Send(alerter.Alert{Priority: alerter.Emergency, Title: title, Description: desc})
}
func (r *recorder) Close(context.Context) error { return nil }

func (r *recorder) got() []alerter.Alert {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]alerter.Alert(nil), r.alerts...)
}

// TestCooldown_AlertsAuthAtOnce: an auth failure alerts on the first
// failure, keyed by the model, because no retry fixes a logged-out CLI.
func TestCooldown_AlertsAuthAtOnce(t *testing.T) {
	ct := NewCooldownTracker()
	rec := &recorder{}
	ct.SetAlerter(rec)

	mark(ct, "claude-cli", testModel, FailoverAuth)
	got := rec.got()
	if len(got) != 1 || got[0].Priority != alerter.Normal || got[0].EventID != ModelKey("claude-cli", testModel) {
		t.Fatalf("auth failure must alert once with the model as event id, got %+v", got)
	}
	mark(ct, "claude-cli", testModel, FailoverBilling)
	if got := rec.got(); len(got) != 2 || got[1].Priority != alerter.Normal {
		t.Fatalf("billing failure must alert (normal priority), got %+v", got)
	}
}

// TestCooldown_AlertsTransientOnlyWhenSettled: transient failures alert,
// and only once the escalation steps are used up.
func TestCooldown_AlertsTransientOnlyWhenSettled(t *testing.T) {
	ct := NewCooldownTracker()
	rec := &recorder{}
	ct.SetAlerter(rec)

	for range len(cooldownEscalation) {
		mark(ct, "openai", testModel, FailoverRateLimit)
	}
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("no alert during escalation, got %+v", got)
	}
	mark(ct, "openai", testModel, FailoverRateLimit)
	got := rec.got()
	if len(got) != 1 || got[0].Priority != alerter.Normal || got[0].EventID != ModelKey("openai", testModel) {
		t.Fatalf("settled cooldown must alert low once, got %+v", got)
	}
}

// TestCooldown_NoAlerterIsFine: a tracker without an alerter keeps working.
func TestCooldown_NoAlerterIsFine(t *testing.T) {
	ct := NewCooldownTracker()
	mark(ct, "openai", testModel, FailoverAuth)
	if ct.IsAvailable("openai", testModel) {
		t.Fatal("model should be parked")
	}
}
