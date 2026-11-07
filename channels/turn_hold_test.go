// ClawEh
// License: MIT

package channels

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/internal/clock"
)

// heldChat is a manager on a fake clock with one editing channel, "test", and
// a chat with a typing indicator, a reaction and a placeholder recorded.
type heldChat struct {
	m        *Manager
	clk      *clock.Fake
	ch       *mockMessageEditor
	edits    []string
	typing   atomic.Bool // stopped
	reaction atomic.Bool // undone
}

func newHeldChat(t *testing.T) *heldChat {
	t.Helper()
	h := &heldChat{clk: clock.NewFake(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))}
	h.m = newTestManager()
	h.m.clock = h.clk
	h.ch = &mockMessageEditor{
		sendFn: func(context.Context, bus.OutboundMessage) error { return nil },
		editFn: func(_ context.Context, _, _, content string) error {
			h.edits = append(h.edits, content)
			return nil
		},
	}
	h.m.channels["test"] = h.ch
	return h
}

func (h *heldChat) record() {
	h.m.RecordTypingStop("test", "c1", func() { h.typing.Store(true) })
	h.m.RecordReactionUndo("test", "c1", "m1", func() { h.reaction.Store(true) })
	h.m.RecordPlaceholder("test", "c1", "ph1")
}

func (h *heldChat) placeholderKept() bool {
	_, ok := h.m.placeholders.Load("test:c1")
	return ok
}

// A turn longer than every TTL keeps its typing indicator, reaction and
// placeholder; the heartbeat still edits the placeholder and the final reply
// still replaces it.
func TestHoldChat_LongTurnKeepsEntries(t *testing.T) {
	h := newHeldChat(t)
	release := h.m.HoldChat("test", "c1")
	h.record()

	for range 4 { // 20 minutes: past typingStopTTL and placeholderTTL
		h.clk.Advance(5 * time.Minute)
		h.m.sweepExpired(h.clk.Now())
		if !h.m.UpdatePlaceholder(context.Background(), "test", "c1", "still working") {
			t.Fatalf("at %v: progress edit found no placeholder", h.clk.Now())
		}
	}
	if h.typing.Load() || h.reaction.Load() || !h.placeholderKept() {
		t.Fatalf("entries evicted during the turn: typing stopped=%v reaction undone=%v placeholder kept=%v",
			h.typing.Load(), h.reaction.Load(), h.placeholderKept())
	}

	// The turn ends and its reply is published; a sweep before the outbound
	// worker sends it leaves everything for the reply.
	release()
	h.clk.Advance(janitorInterval)
	h.m.sweepExpired(h.clk.Now())
	if !h.placeholderKept() || h.typing.Load() || h.reaction.Load() {
		t.Fatal("entries evicted right after the turn ended")
	}

	w := &channelWorker{ch: h.ch, limiter: rate.NewLimiter(rate.Inf, 1)}
	msg := bus.OutboundMessage{Channel: "test", ChatID: "c1", OriginalMessageID: "m1", Content: "the reply"}
	if err := h.m.sendWithRetry(context.Background(), "test", w, msg); err != nil {
		t.Fatalf("sendWithRetry: %v", err)
	}
	if got := h.edits[len(h.edits)-1]; got != "the reply" {
		t.Fatalf("last edit = %q, want the reply", got)
	}
	if len(h.ch.sentMessages) != 0 {
		t.Fatalf("reply sent as a new message: %v", h.ch.sentMessages)
	}
	if !h.typing.Load() || !h.reaction.Load() {
		t.Fatal("reply did not stop typing and undo the reaction")
	}
}

// Entries left behind when no reply consumes them are evicted a TTL after the
// last turn of their chat ended.
func TestHoldChat_EntriesLeftAfterTurnAreEvicted(t *testing.T) {
	h := newHeldChat(t)
	release := h.m.HoldChat("test", "c1")
	h.record()
	h.clk.Advance(30 * time.Minute)
	release()
	release() // a second release changes nothing

	h.clk.Advance(typingStopTTL + time.Second)
	h.m.sweepExpired(h.clk.Now())
	if !h.typing.Load() || !h.reaction.Load() {
		t.Fatal("typing and reaction not evicted a TTL after the turn ended")
	}
	if !h.placeholderKept() {
		t.Fatal("placeholder evicted before its TTL")
	}

	h.clk.Advance(placeholderTTL)
	h.m.sweepExpired(h.clk.Now())
	if h.placeholderKept() {
		t.Fatal("placeholder not evicted a TTL after the turn ended")
	}
	h.m.turnsMu.Lock()
	defer h.m.turnsMu.Unlock()
	if len(h.m.turns) != 0 {
		t.Fatalf("turn record of the chat kept: %v", h.m.turns)
	}
}

// A chat held by two turns keeps its entries until both have ended.
func TestHoldChat_CountsTurns(t *testing.T) {
	h := newHeldChat(t)
	first := h.m.HoldChat("test", "c1")
	second := h.m.HoldChat("test", "c1")
	h.record()
	first()
	h.clk.Advance(placeholderTTL + time.Minute)
	h.m.sweepExpired(h.clk.Now())
	if h.typing.Load() || !h.placeholderKept() {
		t.Fatal("entries evicted while a turn still ran")
	}
	second()
	h.clk.Advance(placeholderTTL + time.Minute)
	h.m.sweepExpired(h.clk.Now())
	if !h.typing.Load() || h.placeholderKept() {
		t.Fatal("entries kept after both turns ended")
	}
}

// Entries of a chat with no turn (leaked: no reply ever consumed them) are
// evicted after their TTL, and a turn in another chat does not keep them.
func TestSweepExpired_EvictsLeakedEntries(t *testing.T) {
	h := newHeldChat(t)
	defer h.m.HoldChat("test", "other")()
	h.record()

	h.clk.Advance(typingStopTTL - time.Second)
	h.m.sweepExpired(h.clk.Now())
	if h.typing.Load() || h.reaction.Load() {
		t.Fatal("evicted before the TTL")
	}

	h.clk.Advance(2 * time.Second)
	h.m.sweepExpired(h.clk.Now())
	if !h.typing.Load() || !h.reaction.Load() {
		t.Fatal("typing and reaction not evicted after typingStopTTL")
	}
	if !h.placeholderKept() {
		t.Fatal("placeholder evicted before placeholderTTL")
	}

	h.clk.Advance(placeholderTTL)
	h.m.sweepExpired(h.clk.Now())
	if h.placeholderKept() {
		t.Fatal("placeholder not evicted after placeholderTTL")
	}
}

// The janitor sweeps on its clock's ticks.
func TestRunTTLJanitor_SweepsOnTick(t *testing.T) {
	h := newHeldChat(t)
	stopped := make(chan struct{})
	h.m.typingStops.Store("test:c1", typingEntry{
		stop:      func() { close(stopped) },
		createdAt: h.clk.Now().Add(-typingStopTTL - time.Minute),
	})

	ctx := t.Context()
	go h.m.runTTLJanitor(ctx)
	if err := h.clk.BlockUntil(ctx, 1); err != nil {
		t.Fatalf("janitor never waited on its ticker: %v", err)
	}
	h.clk.Advance(janitorInterval)

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("janitor did not evict the expired typing entry")
	}
}
