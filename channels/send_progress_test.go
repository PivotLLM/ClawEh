// ClawEh
// License: MIT

package channels

import (
	"context"
	"fmt"
	"testing"

	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/bus"
)

// A retry of one message sees the parts earlier attempts delivered; the next
// message starts from none.
func TestSendWithRetry_KeepsSendProgressAcrossRetries(t *testing.T) {
	m := newTestManager()
	var seen []int
	ch := &mockChannel{
		sendFn: func(ctx context.Context, _ bus.OutboundMessage) error {
			p := SendProgressFrom(ctx)
			seen = append(seen, p.Delivered())
			p.MarkDelivered() // the first part of what is left goes out
			if p.Delivered() < 2 {
				return fmt.Errorf("second part: %w", ErrTemporary)
			}
			return nil
		},
	}
	w := &channelWorker{ch: ch, limiter: rate.NewLimiter(rate.Inf, 1)}
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}

	if err := m.sendWithRetry(context.Background(), "test", w, msg); err != nil {
		t.Fatalf("sendWithRetry: %v", err)
	}
	if err := m.sendWithRetry(context.Background(), "test", w, msg); err != nil {
		t.Fatalf("second sendWithRetry: %v", err)
	}
	if want := []int{0, 1, 0, 1}; fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("delivered parts seen by each attempt = %v, want %v", seen, want)
	}
}

// Without a SendProgress on the context nothing has been delivered and
// nothing is recorded.
func TestSendProgress_Nil(t *testing.T) {
	p := SendProgressFrom(context.Background())
	p.MarkDelivered()
	if p.Delivered() != 0 {
		t.Fatalf("Delivered = %d, want 0", p.Delivered())
	}
}
