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
func (r *alertRecorder) High(string, string, ...string) {}
func (r *alertRecorder) Low(string, string, ...string)  {}
func (r *alertRecorder) Close(context.Context) error    { return nil }

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
	if len(rec.alerts) != 1 || rec.alerts[0].High || rec.alerts[0].EventID != "test" ||
		rec.alerts[0].Title != "Channel send failed" {
		t.Fatalf("dropped message must alert low once for the channel, got %+v", rec.alerts)
	}
}
