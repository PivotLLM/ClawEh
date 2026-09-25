// ClawEh
// License: MIT

package channels

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/tenebris-tech/alerter"
	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/bus"
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

// TestSendWithRetry_AlertsOnGiveUp: a message dropped after its retries
// raises one low alert keyed by the channel; a delivered one raises none.
func TestSendWithRetry_AlertsOnGiveUp(t *testing.T) {
	m := newTestManager()
	rec := &alertRecorder{}
	m.SetAlerter(rec)
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}

	ok := &channelWorker{
		ch:      &mockChannel{sendFn: func(context.Context, bus.OutboundMessage) error { return nil }},
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.sendWithRetry(context.Background(), "test", ok, msg)
	if len(rec.alerts) != 0 {
		t.Fatalf("delivered message must not alert, got %+v", rec.alerts)
	}

	bad := &channelWorker{
		ch: &mockChannel{sendFn: func(context.Context, bus.OutboundMessage) error {
			return fmt.Errorf("bad chat ID: %w", ErrSendFailed)
		}},
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.sendWithRetry(context.Background(), "test", bad, msg)
	if len(rec.alerts) != 1 || rec.alerts[0].Priority != alerter.Normal || rec.alerts[0].EventID != "test" ||
		rec.alerts[0].Title != "Channel send failed" {
		t.Fatalf("dropped message must alert low once for the channel, got %+v", rec.alerts)
	}
}

// TestBaseChannelAlert: Alert is a no-op until an alerter is injected, and an
// empty EventID is filled with the channel name so repeats de-duplicate.
func TestBaseChannelAlert(t *testing.T) {
	ch := NewBaseChannel("telegram-alice", nil, nil, nil)
	ch.Alert(alerter.Alert{Title: "dropped"}) // no alerter: must not panic

	rec := &alertRecorder{}
	ch.SetAlerter(rec)
	ch.Alert(alerter.Alert{Priority: alerter.Urgent, Title: "t"})
	ch.Alert(alerter.Alert{Title: "u", EventID: "explicit"})
	if len(rec.alerts) != 2 {
		t.Fatalf("expected 2 alerts, got %+v", rec.alerts)
	}
	if rec.alerts[0].EventID != "telegram-alice" || rec.alerts[0].Priority != alerter.Urgent || rec.alerts[0].Title != "t" {
		t.Fatalf("empty EventID must become the channel name, got %+v", rec.alerts[0])
	}
	if rec.alerts[1].EventID != "explicit" {
		t.Fatalf("explicit EventID must be kept, got %+v", rec.alerts[1])
	}
}

// TestManagerSetAlerter_PropagatesToChannels: the alerter reaches channels that
// were registered before SetAlerter and ones injected afterwards.
func TestManagerSetAlerter_PropagatesToChannels(t *testing.T) {
	m := newTestManager()
	before := &mockChannel{}
	before.name = "before"
	m.channels["before"] = before

	rec := &alertRecorder{}
	m.SetAlerter(rec)

	after := &mockChannel{}
	after.name = "after"
	m.injectChannelDependencies(after)

	before.Alert(alerter.Alert{Title: "a"})
	after.Alert(alerter.Alert{Title: "b"})
	if len(rec.alerts) != 2 || rec.alerts[0].EventID != "before" || rec.alerts[1].EventID != "after" {
		t.Fatalf("both channels must alert through the manager's alerter, got %+v", rec.alerts)
	}
}
