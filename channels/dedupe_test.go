package channels

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
)

func consumeInbound(t *testing.T, mb *bus.MessageBus) (bus.InboundMessage, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	return mb.ConsumeInbound(ctx)
}

func TestHandleMessage_DropsRedeliveryWithinTTL(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	c := NewBaseChannel("test", nil, mb, []string{"*"})
	now := time.Now()
	c.seen.now = func() time.Time { return now }
	ctx := context.Background()
	peer := bus.Peer{Kind: "direct", ID: "u1"}

	c.HandleMessage(ctx, peer, "m1", "u1", "chat", "hello", nil, nil)
	c.HandleMessage(ctx, peer, "m1", "u1", "chat", "hello again", nil, nil)
	if msg, ok := consumeInbound(t, mb); !ok || msg.Content != "hello" {
		t.Fatalf("first delivery: got %+v ok=%v", msg, ok)
	}
	if msg, ok := consumeInbound(t, mb); ok {
		t.Fatalf("redelivery within TTL must be dropped, got %+v", msg)
	}

	// The same id in another chat is a different message.
	c.HandleMessage(ctx, peer, "m1", "u1", "other-chat", "elsewhere", nil, nil)
	if msg, ok := consumeInbound(t, mb); !ok || msg.Content != "elsewhere" {
		t.Fatalf("same id in another chat: got %+v ok=%v", msg, ok)
	}

	// After the TTL it is accepted again.
	now = now.Add(inboundDedupeTTL)
	c.HandleMessage(ctx, peer, "m1", "u1", "chat", "late", nil, nil)
	if msg, ok := consumeInbound(t, mb); !ok || msg.Content != "late" {
		t.Fatalf("after TTL: got %+v ok=%v", msg, ok)
	}
}

func TestHandleMessage_EmptyIDNeverDeduped(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	c := NewBaseChannel("test", nil, mb, []string{"*"})
	ctx := context.Background()
	peer := bus.Peer{Kind: "direct", ID: "u1"}

	c.HandleMessage(ctx, peer, "", "u1", "chat", "one", nil, nil)
	c.HandleMessage(ctx, peer, "", "u1", "chat", "two", nil, nil)
	for _, want := range []string{"one", "two"} {
		if msg, ok := consumeInbound(t, mb); !ok || msg.Content != want {
			t.Fatalf("want %q, got %+v ok=%v", want, msg, ok)
		}
	}
}

func TestInboundDedupe_EvictsOldestAtCapacity(t *testing.T) {
	d := newInboundDedupe()
	for i := range inboundDedupeSize + 1 {
		if d.duplicate(strconv.Itoa(i)) {
			t.Fatalf("fresh key %d reported duplicate", i)
		}
	}
	if !d.duplicate("1") {
		t.Error("second key should still be remembered")
	}
	// Checking "0" re-records it (and evicts the next oldest), so test it last.
	if d.duplicate("0") {
		t.Error("oldest key should have been evicted")
	}
	if len(d.seen) != inboundDedupeSize || len(d.order) != inboundDedupeSize {
		t.Errorf("size = %d/%d, want %d", len(d.seen), len(d.order), inboundDedupeSize)
	}
}

func TestInboundDedupe_NilSafe(t *testing.T) {
	var d *inboundDedupe
	if d.duplicate("x") {
		t.Error("nil dedupe must never drop")
	}
}
