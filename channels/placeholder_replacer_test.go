// ClawEh
// License: MIT

package channels

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/bus"
)

// replacerChannel is a channel whose placeholder is replaced with the whole
// reply (PlaceholderReplacer) and that splits messages at maxLen.
type replacerChannel struct {
	mockChannelWithLength
	replaced   []bus.OutboundMessage
	replaceErr error
}

func (c *replacerChannel) ReplacePlaceholder(_ context.Context, placeholderID string, msg bus.OutboundMessage) error {
	if placeholderID != "ph1" {
		return errors.New("unexpected placeholder " + placeholderID)
	}
	c.replaced = append(c.replaced, msg)
	return c.replaceErr
}

// A split reply replaces the placeholder with its first chunk, as a whole
// message, and sends the others normally.
func TestSendSplit_ReplacerTakesFirstChunk(t *testing.T) {
	m := newTestManager()
	ch := &replacerChannel{maxLen: 10}
	ch.sendFn = func(context.Context, bus.OutboundMessage) error { return nil }
	m.RecordPlaceholder("test", "1", "ph1")
	w := &channelWorker{ch: ch, limiter: rate.NewLimiter(rate.Inf, 1)}
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", OriginalMessageID: "m1", Content: "aaaa bbbb cccc dddd"}

	if err := m.sendSplit(context.Background(), "test", w, msg); err != nil {
		t.Fatalf("sendSplit: %v", err)
	}
	if len(ch.replaced) != 1 {
		t.Fatalf("placeholder replaced %d times, want 1", len(ch.replaced))
	}
	if got := ch.replaced[0]; got.OriginalMessageID != "m1" || got.Content == "" || got.Content == msg.Content {
		t.Fatalf("replaced with %+v, want the first chunk of the reply", got)
	}
	if len(ch.sentMessages) == 0 {
		t.Fatal("the remaining chunks were not sent")
	}
	if ch.editedMessages != 0 {
		t.Fatalf("EditMessage used %d times for the reply, want ReplacePlaceholder only", ch.editedMessages)
	}
}

// A failed replacement falls back to sending the reply.
func TestPreSend_ReplacerFailureSends(t *testing.T) {
	m := newTestManager()
	ch := &replacerChannel{replaceErr: ErrTemporary}
	ch.sendFn = func(context.Context, bus.OutboundMessage) error { return nil }
	m.RecordPlaceholder("test", "1", "ph1")
	w := &channelWorker{ch: ch, limiter: rate.NewLimiter(rate.Inf, 1)}

	if err := m.sendWithRetry(context.Background(), "test", w, bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hi"}); err != nil {
		t.Fatalf("sendWithRetry: %v", err)
	}
	if len(ch.sentMessages) != 1 {
		t.Fatalf("sent %d messages, want the reply sent once", len(ch.sentMessages))
	}
}
