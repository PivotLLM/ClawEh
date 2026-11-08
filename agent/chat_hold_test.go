// ClawEh
// License: MIT

package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
)

// fakeChatHolder records the holds runQueuedTurn takes: how many are open per
// "channel:chatID", and each release on released.
type fakeChatHolder struct {
	mu       sync.Mutex
	open     map[string]int
	holds    map[string]int
	released chan string
}

func newFakeChatHolder() *fakeChatHolder {
	return &fakeChatHolder{open: map[string]int{}, holds: map[string]int{}, released: make(chan string, 16)}
}

func (h *fakeChatHolder) HoldChat(channel, chatID string) func() {
	key := channel + ":" + chatID
	h.mu.Lock()
	h.open[key]++
	h.holds[key]++
	h.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			h.open[key]--
			h.mu.Unlock()
			h.released <- key
		})
	}
}

// state is the open holds and the holds ever taken for key.
func (h *fakeChatHolder) state(key string) (open, holds int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.open[key], h.holds[key]
}

// awaitRelease fails the test unless key is released within a generous bound.
func (h *fakeChatHolder) awaitRelease(t *testing.T, key string) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case got := <-h.released:
			if got == key {
				return
			}
			t.Fatalf("released %s, want %s", got, key)
		case <-deadline:
			t.Fatalf("timed out waiting for the hold of %s to be released", key)
		}
	}
}

// A queued turn holds its chat for its whole life: from before it waits for
// a concurrent-turn slot, through the run, until its reply is out; then the
// hold is released, once.
func TestRunQueuedTurn_HoldsChatForWholeTurn(t *testing.T) {
	al, msgBus, p := newBlockingLoop(t, twoAgentConfig(t, 1))
	holder := newFakeChatHolder()
	al.chatHolder = holder
	var slotWait <-chan struct{}
	al.onWait, slotWait = waitSignals(waitTurnSlot)

	expect := func(key string, open, holds int, when string) {
		t.Helper()
		if gotOpen, gotHolds := holder.state(key); gotOpen != open || gotHolds != holds {
			t.Fatalf("%s: %s has %d open of %d holds, want %d of %d", when, key, gotOpen, gotHolds, open, holds)
		}
	}

	go dispatch(al, bus.InboundMessage{Channel: "cha", ChatID: "x", SenderID: "u", Content: "first"})
	p.waitStarted(t)
	expect("cha:x", 1, 1, "while the first turn runs")

	go dispatch(al, bus.InboundMessage{Channel: "chb", ChatID: "y", SenderID: "u", Content: "second"})
	awaitSignal(t, slotWait, "the second turn to wait for the slot")
	expect("chb:y", 1, 1, "while the second turn waits for the slot")
	if n := p.callCount(); n != 1 {
		t.Fatalf("model calls while the second turn waits = %d, want 1", n)
	}

	p.release <- struct{}{}
	if out := nextOutbound(t, msgBus); out.Channel != "cha" {
		t.Fatalf("first reply on %q, want cha", out.Channel)
	}
	holder.awaitRelease(t, "cha:x")
	expect("cha:x", 0, 1, "after the first turn")

	p.waitStarted(t)
	expect("chb:y", 1, 1, "while the second turn runs")
	p.release <- struct{}{}
	if out := nextOutbound(t, msgBus); out.Channel != "chb" {
		t.Fatalf("second reply on %q, want chb", out.Channel)
	}
	holder.awaitRelease(t, "chb:y")
	al.activeRequests.Wait()
	expect("cha:x", 0, 1, "at the end")
	expect("chb:y", 0, 1, "at the end")
}

// A turn cancelled while it waits for a slot releases its hold too.
func TestRunQueuedTurn_ReleasesHoldWhenCancelledWaiting(t *testing.T) {
	al, msgBus, p := newBlockingLoop(t, twoAgentConfig(t, 1))
	holder := newFakeChatHolder()
	al.chatHolder = holder
	var slotWait <-chan struct{}
	al.onWait, slotWait = waitSignals(waitTurnSlot)

	go dispatch(al, bus.InboundMessage{Channel: "cha", ChatID: "x", SenderID: "u", Content: "first"})
	p.waitStarted(t)
	go dispatch(al, bus.InboundMessage{Channel: "chb", ChatID: "y", SenderID: "u", Content: "second"})
	awaitSignal(t, slotWait, "the second turn to wait for the slot")

	dispatch(al, bus.InboundMessage{Channel: "chb", ChatID: "y", SenderID: "u", Content: "/cancel"})
	holder.awaitRelease(t, "chb:y")
	if open, holds := holder.state("chb:y"); open != 0 || holds != 1 {
		t.Fatalf("chb:y has %d open of %d holds after the cancel, want 0 of 1", open, holds)
	}
	if n := p.callCount(); n != 1 {
		t.Fatalf("model calls = %d, want 1 (the cancelled turn never ran)", n)
	}

	p.release <- struct{}{}
	for range 2 { // the cancel report and the first turn's reply
		nextOutbound(t, msgBus)
	}
	holder.awaitRelease(t, "cha:x")
	al.activeRequests.Wait()
}
