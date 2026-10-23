package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/bus"
)

// rateLimitedFor is a send refused with ErrRateLimit carrying a retry-after.
func rateLimitedFor(after time.Duration) error {
	return WithRetryAfter(fmt.Errorf("429: retry after %d: %w", int(after.Seconds()), ErrRateLimit), after)
}

// A send refused with a retry-after up to MaxSendRetryAfter waits it plus
// RetryAfterPadding, then is sent again.
func TestSendWithRetry_RetryAfterWaits(t *testing.T) {
	for _, tc := range []struct {
		after, want time.Duration
	}{
		{10 * time.Second, 15 * time.Second},
		{60 * time.Second, 65 * time.Second},
	} {
		t.Run(tc.after.String(), func(t *testing.T) {
			m := newTestManager()
			fc := newRecordingClock(false)
			m.clock = fc
			var calls atomic.Int32
			w := &channelWorker{
				ch: &mockChannel{sendFn: func(context.Context, bus.OutboundMessage) error {
					if calls.Add(1) == 1 {
						return rateLimitedFor(tc.after)
					}
					return nil
				}},
				limiter: rate.NewLimiter(rate.Inf, 1),
			}
			msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}
			result := make(chan error, 1)
			go func() { result <- m.sendWithRetry(context.Background(), "test", w, msg) }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := fc.BlockUntil(ctx, 1); err != nil {
				t.Fatalf("never waited: %v", err)
			}
			if got := fc.Waits(); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("waits = %v, want [%v]", got, tc.want)
			}
			fc.Advance(tc.want - time.Nanosecond)
			if calls.Load() != 1 {
				t.Fatal("sent again before the wait ended")
			}
			fc.Advance(time.Nanosecond)
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("sendWithRetry: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("sendWithRetry did not return after the wait")
			}
			if calls.Load() != 2 {
				t.Fatalf("Send calls = %d, want 2", calls.Load())
			}
		})
	}
}

// A retry-after longer than MaxSendRetryAfter fails the send at once, with
// the "Channel send failed" alert carrying the platform's reason.
func TestSendWithRetry_RetryAfterTooLongGivesUp(t *testing.T) {
	m := newTestManager()
	rec := &alertRecorder{}
	m.SetAlerter(rec)
	var calls atomic.Int32
	w := &channelWorker{
		ch: &mockChannel{sendFn: func(context.Context, bus.OutboundMessage) error {
			calls.Add(1)
			return rateLimitedFor(61 * time.Second)
		}},
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}
	if err := m.sendWithRetry(context.Background(), "test", w, msg); !errors.Is(err, ErrRateLimit) {
		t.Fatalf("sendWithRetry = %v, want ErrRateLimit", err)
	}
	if calls.Load() != 1 || len(waitsOf(m)) != 0 {
		t.Fatalf("calls = %d, waits = %v; want one call and no wait", calls.Load(), waitsOf(m))
	}
	if len(rec.alerts) != 1 || rec.alerts[0].Title != "Channel send failed" ||
		!strings.Contains(rec.alerts[0].Details, "retry after 61") {
		t.Fatalf("alerts = %+v, want one Channel send failed with the reason", rec.alerts)
	}
}

// A context cancelled during a retry-after wait ends the send at once.
func TestSendWithRetry_RetryAfterCancelled(t *testing.T) {
	m := newTestManager()
	fc := newRecordingClock(false)
	m.clock = fc
	var calls atomic.Int32
	w := &channelWorker{
		ch: &mockChannel{sendFn: func(context.Context, bus.OutboundMessage) error {
			calls.Add(1)
			return rateLimitedFor(30 * time.Second)
		}},
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}
	sendCtx, cancelSend := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- m.sendWithRetry(sendCtx, "test", w, msg) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := fc.BlockUntil(ctx, 1); err != nil {
		t.Fatalf("never waited: %v", err)
	}
	cancelSend()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("sendWithRetry = %v, want context.Canceled", err)
		}
	case <-ctx.Done():
		t.Fatal("sendWithRetry did not return after the cancel")
	}
	if calls.Load() != 1 {
		t.Fatalf("Send calls = %d, want 1", calls.Load())
	}
}
