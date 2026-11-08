// ClawEh
// License: MIT

package channels

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/internal/clock"
)

// countingMediaMock is a MediaSender safe for concurrent use.
type countingMediaMock struct {
	mockChannel
	calls atomic.Int32
}

func (m *countingMediaMock) SendMedia(context.Context, bus.OutboundMediaMessage) error {
	m.calls.Add(1)
	return nil
}

// On shutdown every file still queued, and every held file the worker had
// not sent, is reported to its sender as not sent; none is sent.
func TestRunMediaWorker_ShutdownReportsUnsentFiles(t *testing.T) {
	m := newTestManager()
	ch := &countingMediaMock{}
	const queued, backlog = 3, 2
	w := &channelWorker{
		ch: ch, mediaQueue: make(chan bus.OutboundMediaMessage, queued), mediaDone: make(chan struct{}),
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	reports := make(chan error, queued+backlog)
	onDelivery := func(err error) { reports <- err }
	for range backlog {
		w.mediaBacklog = append(w.mediaBacklog, bus.OutboundMediaMessage{Channel: "test", ChatID: "1", OnDelivery: onDelivery})
	}
	for range queued {
		w.mediaQueue <- bus.OutboundMediaMessage{Channel: "test", ChatID: "1", OnDelivery: onDelivery}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		m.runMediaWorker(ctx, "test", w)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("media worker did not stop on shutdown")
	}

	for i := range queued + backlog {
		select {
		case err := <-reports:
			if err == nil {
				t.Fatalf("report %d = nil, want a not-sent error", i)
			}
		default:
			t.Fatalf("only %d of %d files reported", i, queued+backlog)
		}
	}
	if n := ch.calls.Load(); n != 0 {
		t.Fatalf("SendMedia called %d times after shutdown", n)
	}
}

// A closed queue ends the worker without reporting anything twice.
func TestRunMediaWorker_ClosedQueueEnds(t *testing.T) {
	m := newTestManager()
	ch := &countingMediaMock{}
	w := &channelWorker{
		ch: ch, mediaQueue: make(chan bus.OutboundMediaMessage, 1), mediaDone: make(chan struct{}),
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	reports, onDelivery := deliveryReport()
	w.mediaQueue <- bus.OutboundMediaMessage{Channel: "test", ChatID: "1", OnDelivery: onDelivery}
	close(w.mediaQueue)
	done := make(chan struct{})
	go func() {
		m.runMediaWorker(t.Context(), "test", w)
		close(done)
	}()
	if err := expectDelivery(t, reports); err != nil {
		t.Fatalf("delivery = %v, want sent", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not end when its queue closed")
	}
	if len(reports) != 0 {
		t.Fatalf("extra reports: %d", len(reports))
	}
}

// Turns starting and ending in many chats while the janitor sweeps and
// indicators are recorded: no race, a chat with a running turn keeps its
// entries, and once every turn has ended and the TTL passed everything is
// evicted and the turn records are forgotten.
func TestHoldChat_ConcurrentTurnsAndSweeps(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC))
	m := newTestManager()
	m.clock = clk

	var stops atomic.Int32
	keep := m.HoldChat("test", "kept")
	m.RecordTypingStop("test", "kept", func() { stops.Add(1) })
	m.RecordPlaceholder("test", "kept", "ph")

	chats := []string{"a", "b", "c", "d"}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chat := chats[i%len(chats)]
			for range 50 {
				release := m.HoldChat("test", chat)
				m.RecordTypingStop("test", chat, func() { stops.Add(1) })
				m.RecordReactionUndo("test", chat, "m", func() {})
				m.RecordPlaceholder("test", chat, "p")
				release()
				release() // a second release does nothing
			}
		}(i)
	}
	sweeperDone := make(chan struct{})
	go func() {
		defer close(sweeperDone)
		for range 50 {
			m.sweepExpired(clk.Now().Add(time.Hour))
		}
	}()
	wg.Wait()
	<-sweeperDone

	// Every chat but "kept" has no running turn: after the TTL past their
	// last end everything of theirs goes; "kept" keeps its own.
	clk.Advance(placeholderTTL + time.Second)
	m.sweepExpired(clk.Now())
	for _, chat := range chats {
		if _, ok := m.placeholders.Load("test:" + chat); ok {
			t.Errorf("placeholder of %s kept after its turns ended", chat)
		}
		if _, ok := m.typingStops.Load("test:" + chat); ok {
			t.Errorf("typing of %s kept after its turns ended", chat)
		}
	}
	if _, ok := m.placeholders.Load("test:kept"); !ok {
		t.Fatal("placeholder of a chat with a running turn was evicted")
	}
	if _, ok := m.typingStops.Load("test:kept"); !ok {
		t.Fatal("typing of a chat with a running turn was stopped")
	}

	keep()
	clk.Advance(placeholderTTL + time.Second)
	m.sweepExpired(clk.Now())
	if _, ok := m.placeholders.Load("test:kept"); ok {
		t.Fatal("placeholder kept a TTL after its last turn ended")
	}
	// The ended turns are forgotten once no entry can age from them.
	clk.Advance(placeholderTTL + time.Second)
	m.sweepExpired(clk.Now())
	m.turnsMu.Lock()
	left := len(m.turns)
	m.turnsMu.Unlock()
	if left != 0 {
		t.Fatalf("turn records left = %d, want 0", left)
	}
}
