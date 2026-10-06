package channels

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/bus"
)

// deliveryReport returns a buffered channel and an OnDelivery that writes to it.
func deliveryReport() (chan error, func(error)) {
	ch := make(chan error, 2)
	return ch, func(err error) { ch <- err }
}

func expectDelivery(t *testing.T, reports chan error) error {
	t.Helper()
	select {
	case err := <-reports:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("OnDelivery was not called")
		return nil
	}
}

// The worker reports how each message's delivery ended: nil when sent, the
// send error when the channel gave up.
func TestRunWorker_ReportsDelivery(t *testing.T) {
	tests := []struct {
		name    string
		sendErr error
		wantErr bool
	}{
		{name: "delivered", sendErr: nil, wantErr: false},
		{name: "send failed", sendErr: ErrSendFailed, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestManager()
			ch := &mockChannel{sendFn: func(context.Context, bus.OutboundMessage) error { return tt.sendErr }}
			w := &channelWorker{
				ch: ch, queue: make(chan bus.OutboundMessage, 1), done: make(chan struct{}),
				limiter: rate.NewLimiter(rate.Inf, 1),
			}
			go m.runWorker(t.Context(), "test", w)

			reports, onDelivery := deliveryReport()
			w.queue <- bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hi", OnDelivery: onDelivery}
			err := expectDelivery(t, reports)
			if (err != nil) != tt.wantErr {
				t.Fatalf("delivery error = %v, want error %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, ErrSendFailed) {
				t.Fatalf("delivery error = %v, want ErrSendFailed", err)
			}
		})
	}
}

// A message the dispatcher cannot hand to any channel is reported as failed.
func TestDispatchOutbound_ReportsUnknownChannel(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	m := &Manager{channels: make(map[string]Channel), workers: make(map[string]*channelWorker), bus: mb}
	go m.dispatchOutbound(t.Context())

	reports, onDelivery := deliveryReport()
	if err := mb.PublishOutbound(t.Context(), bus.OutboundMessage{Channel: "nowhere", ChatID: "1", Content: "hi", OnDelivery: onDelivery}); err != nil {
		t.Fatal(err)
	}
	if err := expectDelivery(t, reports); err == nil {
		t.Fatal("an unknown channel must be reported as a failed delivery")
	}
}
