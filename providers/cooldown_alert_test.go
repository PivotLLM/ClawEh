// ClawEh
// License: MIT

package providers

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/alerts"
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

// alertStart is a fixed start for the alert tests, so the text an alert
// carries about it can be compared.
var alertStart = time.Date(2026, time.November, 14, 9, 2, 0, 0, time.UTC)

// newAlertTracker is a tracker on a clock the test moves, with a recorder as
// its alerter.
func newAlertTracker() (*CooldownTracker, *time.Time, *recorder) {
	ct, current := newTestTracker(alertStart)
	rec := &recorder{}
	ct.SetAlerter(rec)
	return ct, current, rec
}

// TestCooldown_AlertsAuthAtOnce: an auth failure alerts on the first
// failure, keyed by the model, because no retry fixes a logged-out CLI; so
// does a billing failure of another model.
func TestCooldown_AlertsAuthAtOnce(t *testing.T) {
	ct, _, rec := newAlertTracker()

	mark(ct, "claude-cli", testModel, FailoverAuth)
	got := rec.got()
	if len(got) != 1 || got[0].Priority != alerter.Normal || got[0].Title != alertModelAuth ||
		got[0].EventID != ModelKey("claude-cli", testModel) {
		t.Fatalf("auth failure must alert once with the model as event id, got %+v", got)
	}
	if strings.Contains(got[0].Description, "ongoing") {
		t.Fatalf("a first alert is not a reminder, got %q", got[0].Description)
	}
	mark(ct, "openrouter", testModel, FailoverBilling)
	if got := rec.got(); len(got) != 2 || got[1].Priority != alerter.Normal || got[1].Title != alertModelAuth {
		t.Fatalf("billing failure of another model must alert (normal priority), got %+v", got)
	}
}

// TestCooldown_RepeatsHeldForADay: a model that stays out of credit is parked
// again each time its cooldown ends, but alerts once, then once a day with
// the start of the trouble and the count of parks held back.
func TestCooldown_RepeatsHeldForADay(t *testing.T) {
	ct, current, rec := newAlertTracker()
	park := DefaultCooldownPolicy().BillingAuth

	mark(ct, "openrouter", testModel, FailoverBilling)
	held := 0
	for current.Sub(alertStart)+park < alerts.RemindEvery {
		*current = current.Add(park)
		mark(ct, "openrouter", testModel, FailoverBilling)
		held++
	}
	// An auth failure of the same model is the same trouble, not a new alert.
	mark(ct, "openrouter", testModel, FailoverAuth)
	held++
	if got := rec.got(); len(got) != 1 {
		t.Fatalf("re-parks inside a day must not alert again, got %d alerts: %+v", len(got), got)
	}

	*current = alertStart.Add(alerts.RemindEvery)
	mark(ct, "openrouter", testModel, FailoverBilling)
	got := rec.got()
	if len(got) != 2 || got[1].Title != alertModelAuth || got[1].EventID != ModelKey("openrouter", testModel) {
		t.Fatalf("a day later the model must alert once more, got %+v", got)
	}
	want := "; ongoing since Nov 14 09:02, " + strconv.Itoa(held) + " more since the last alert"
	if !strings.HasSuffix(got[1].Description, want) {
		t.Fatalf("reminder = %q, want it to end %q", got[1].Description, want)
	}
}

// TestCooldown_RecoveryAlertsOnceAndStartsAfresh: a model alerted as failing
// that answers again raises one "working again" alert, and its next failure
// is a new incident that alerts at once.
func TestCooldown_RecoveryAlertsOnceAndStartsAfresh(t *testing.T) {
	ct, current, rec := newAlertTracker()
	id := ModelKey("openrouter", testModel)

	mark(ct, "openrouter", testModel, FailoverBilling)
	*current = current.Add(2 * time.Hour)
	ct.MarkSuccess("openrouter", testModel)

	got := rec.got()
	if len(got) != 2 || got[1].Title != alertModelUp || got[1].EventID != id+"-up" || got[1].Priority != alerter.Normal {
		t.Fatalf("recovery must raise one working-again alert, got %+v", got)
	}
	if want := id + " answered again; it had been failing since Nov 14 09:02"; got[1].Description != want {
		t.Fatalf("recovery description = %q, want %q", got[1].Description, want)
	}

	ct.MarkSuccess("openrouter", testModel)
	if got = rec.got(); len(got) != 2 {
		t.Fatalf("a model that keeps working must not alert again, got %+v", got)
	}

	*current = current.Add(time.Hour)
	mark(ct, "openrouter", testModel, FailoverBilling)
	got = rec.got()
	if len(got) != 3 || got[2].Title != alertModelAuth || strings.Contains(got[2].Description, "ongoing") {
		t.Fatalf("a failure after recovery must alert as a new incident, got %+v", got)
	}
}

// TestCooldown_NoRecoveryAlertWithoutAnAlert: a success tells the operator
// nothing when the model was never alerted as failing, whether it never
// failed or failed without settling.
func TestCooldown_NoRecoveryAlertWithoutAnAlert(t *testing.T) {
	ct, _, rec := newAlertTracker()

	ct.MarkSuccess("openai", testModel)
	mark(ct, "openai", testModel, FailoverRateLimit) // one transient failure: no alert
	ct.MarkSuccess("openai", testModel)
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("no alert was raised, so none may say it is over, got %+v", got)
	}
}

// TestCooldown_ClearRearmsTheAlert: clearing a cooldown keeps the incident
// but lets the next failure alert at once, so an operator who thought it
// fixed hears otherwise; a success after the clear still reports recovery.
func TestCooldown_ClearRearmsTheAlert(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear func(*CooldownTracker)
	}{
		{"one model", func(ct *CooldownTracker) { ct.Clear("openrouter", testModel) }},
		{"all models", func(ct *CooldownTracker) { ct.ClearAll() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ct, current, rec := newAlertTracker()
			mark(ct, "openrouter", testModel, FailoverBilling)

			*current = current.Add(10 * time.Minute)
			tc.clear(ct)
			mark(ct, "openrouter", testModel, FailoverBilling)
			got := rec.got()
			if len(got) != 2 || got[1].Title != alertModelAuth ||
				!strings.HasSuffix(got[1].Description, "; ongoing since Nov 14 09:02") {
				t.Fatalf("a failure after a clear must alert at once as ongoing, got %+v", got)
			}

			*current = current.Add(10 * time.Minute)
			tc.clear(ct)
			ct.MarkSuccess("openrouter", testModel)
			got = rec.got()
			if len(got) != 3 || got[2].Title != alertModelUp || !strings.HasSuffix(got[2].Description, "since Nov 14 09:02") {
				t.Fatalf("a success after a clear must report recovery, got %+v", got)
			}
		})
	}
}

// TestCooldown_SettledAndAuthAreSeparateIncidents: a model alerted as parked
// for rate limits that then loses its credit alerts for that too, and one
// recovery covers both, from the earlier start.
func TestCooldown_SettledAndAuthAreSeparateIncidents(t *testing.T) {
	ct, current, rec := newAlertTracker()

	for range len(cooldownEscalation) + 1 {
		mark(ct, "openai", testModel, FailoverRateLimit)
	}
	*current = current.Add(time.Hour)
	mark(ct, "openai", testModel, FailoverRateLimit) // still parked: held
	mark(ct, "openai", testModel, FailoverBilling)
	got := rec.got()
	if len(got) != 2 || got[0].Title != alertModelParked || got[1].Title != alertModelAuth {
		t.Fatalf("want the parked alert, then the billing alert, got %+v", got)
	}

	ct.MarkSuccess("openai", testModel)
	got = rec.got()
	if len(got) != 3 || got[2].Title != alertModelUp || !strings.HasSuffix(got[2].Description, "since Nov 14 09:02") {
		t.Fatalf("one recovery from the earlier start must cover both, got %+v", got)
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
