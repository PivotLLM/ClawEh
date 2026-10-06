package channels

import (
	"context"
	"errors"
	"fmt"
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
	if err := expectDelivery(t, reports); !errors.Is(err, ErrUnknownChannel) {
		t.Fatalf("delivery error = %v, want ErrUnknownChannel", err)
	}
}

// Messages still queued for their channel when the service stops are
// reported as not delivered, each exactly once.
func TestRunWorker_ReportsQueuedAtShutdown(t *testing.T) {
	m := newTestManager()
	ch := &mockChannel{sendFn: func(context.Context, bus.OutboundMessage) error { return nil }}
	const queued = 3
	w := &channelWorker{
		ch: ch, queue: make(chan bus.OutboundMessage, queued), done: make(chan struct{}),
		limiter: rate.NewLimiter(1, 1),
	}
	reports := make(chan error, queued+1)
	for range queued {
		w.queue <- bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hi", OnDelivery: func(err error) { reports <- err }}
	}
	// Spend the limiter's token so a send attempted after the stop waits on
	// the cancelled context instead of going through.
	w.limiter.Allow()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m.runWorker(ctx, "test", w)
	if len(reports) != queued {
		t.Fatalf("%d reports for %d queued messages", len(reports), queued)
	}
	for range queued {
		if err := <-reports; err == nil {
			t.Fatal("a message queued at shutdown was reported delivered")
		}
	}
}

// SendMessage reports its result to OnDelivery as well as returning it.
func TestSendMessage_ReportsDelivery(t *testing.T) {
	m := newTestManager()
	reports, onDelivery := deliveryReport()
	err := m.SendMessage(t.Context(), bus.OutboundMessage{Channel: "nowhere", ChatID: "1", Content: "hi", OnDelivery: onDelivery})
	if err == nil {
		t.Fatal("an unknown channel must fail")
	}
	if got := expectDelivery(t, reports); got == nil || got.Error() != err.Error() {
		t.Fatalf("OnDelivery got %v, want %v", got, err)
	}
}

// Every failed send reaches OnDelivery with its reason, testable with
// errors.Is, and only a channel that is not running or a send that failed
// after its retries raises the "Channel send failed" alert. A recipient that
// is offline or does not exist is not retried.
func TestSendMessage_ReasonsAndAlerts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		channel   string // "" uses the registered channel
		noWorker  bool
		sendErr   error
		want      error
		wantAlert bool
		wantCalls int
	}{
		{name: "unknown channel", channel: "nowhere", want: ErrUnknownChannel},
		{name: "no worker", noWorker: true, want: ErrNotRunning},
		{name: "channel not running", sendErr: ErrNotRunning, want: ErrNotRunning, wantAlert: true, wantCalls: 1},
		{name: "recipient offline", sendErr: fmt.Errorf("device:1: %w", ErrRecipientOffline), want: ErrRecipientOffline, wantCalls: 1},
		{name: "recipient not found", sendErr: fmt.Errorf("chat not found: %w", ErrRecipientNotFound), want: ErrRecipientNotFound, wantCalls: 1},
		{name: "send failed", sendErr: fmt.Errorf("bad request: %w", ErrSendFailed), want: ErrSendFailed, wantAlert: true, wantCalls: 1},
		{name: "failed after retries", sendErr: fmt.Errorf("timeout: %w", ErrTemporary), want: ErrTemporary, wantAlert: true, wantCalls: maxRetries + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newTestManager()
			rec := &alertRecorder{}
			m.SetAlerter(rec)
			calls := 0
			ch := &mockChannel{sendFn: func(context.Context, bus.OutboundMessage) error {
				calls++
				return tt.sendErr
			}}
			m.channels["test"] = ch
			if !tt.noWorker {
				m.workers["test"] = &channelWorker{ch: ch, limiter: rate.NewLimiter(rate.Inf, 1)}
			}
			name := tt.channel
			if name == "" {
				name = "test"
			}

			reports, onDelivery := deliveryReport()
			err := m.SendMessage(t.Context(), bus.OutboundMessage{Channel: name, ChatID: "1", Content: "hi", OnDelivery: onDelivery})
			if !errors.Is(err, tt.want) {
				t.Fatalf("SendMessage = %v, want %v", err, tt.want)
			}
			if got := expectDelivery(t, reports); !errors.Is(got, tt.want) {
				t.Fatalf("OnDelivery got %v, want %v", got, tt.want)
			}
			if calls != tt.wantCalls {
				t.Fatalf("Send called %d times, want %d", calls, tt.wantCalls)
			}
			rec.mu.Lock()
			defer rec.mu.Unlock()
			if alerted := len(rec.alerts) > 0; alerted != tt.wantAlert {
				t.Fatalf("alerts = %+v, want alert %v", rec.alerts, tt.wantAlert)
			}
			if tt.wantAlert && (len(rec.alerts) != 1 || rec.alerts[0].Title != "Channel send failed") {
				t.Fatalf("alerts = %+v, want one Channel send failed", rec.alerts)
			}
		})
	}
}

// The dispatcher reports a message on a channel with no worker as not running.
func TestDispatchOutbound_ReportsNoWorkerAsNotRunning(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	m := &Manager{channels: map[string]Channel{"test": &mockChannel{}}, workers: make(map[string]*channelWorker), bus: mb}
	go m.dispatchOutbound(t.Context())

	reports, onDelivery := deliveryReport()
	if err := mb.PublishOutbound(t.Context(), bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hi", OnDelivery: onDelivery}); err != nil {
		t.Fatal(err)
	}
	if err := expectDelivery(t, reports); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("delivery error = %v, want ErrNotRunning", err)
	}
}
