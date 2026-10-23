// ClawEh
// License: MIT

package agent

import (
	"context"
	"sync"
	"testing"
)

// slotLoop is a loop with n concurrent-turn slots and nothing else.
func slotLoop(n int) *AgentLoop {
	return &AgentLoop{turnSem: make(chan struct{}, n)}
}

// slotsInUse is how many of al's slots are taken.
func slotsInUse(al *AgentLoop) int { return len(al.turnSem) }

func TestTurnSlot_LendAndReclaim(t *testing.T) {
	al := slotLoop(1)
	s := &turnSlot{al: al}
	if !s.acquire(context.Background()) || slotsInUse(al) != 1 {
		t.Fatal("acquire did not take the slot")
	}
	s.lend()
	if slotsInUse(al) != 0 {
		t.Fatal("lend did not give the slot up")
	}
	s.reclaim(context.Background())
	if slotsInUse(al) != 1 {
		t.Fatal("reclaim did not take the slot back")
	}
	s.release()
	s.release() // a second release is a no-op
	if slotsInUse(al) != 0 {
		t.Fatalf("slots in use after release = %d", slotsInUse(al))
	}
}

// Two waits at once (parallel tool calls): the first lends, the last
// reclaims, and nothing is released twice.
func TestTurnSlot_ParallelWaits(t *testing.T) {
	al := slotLoop(1)
	s := &turnSlot{al: al}
	s.acquire(context.Background())
	s.lend()
	s.lend()
	if slotsInUse(al) != 0 {
		t.Fatal("the slot was not lent")
	}
	s.reclaim(context.Background())
	if slotsInUse(al) != 0 {
		t.Fatal("reclaimed while another wait was still in progress")
	}
	s.reclaim(context.Background())
	if slotsInUse(al) != 1 {
		t.Fatal("the last wait did not reclaim the slot")
	}
	s.release()
	if slotsInUse(al) != 0 {
		t.Fatal("slot leaked")
	}
}

// Concurrent lend/reclaim pairs never leak or double-release a slot.
func TestTurnSlot_ConcurrentWaits(t *testing.T) {
	al := slotLoop(1)
	s := &turnSlot{al: al}
	s.acquire(context.Background())
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			s.lend()
			s.reclaim(context.Background())
		})
	}
	wg.Wait()
	if slotsInUse(al) != 1 {
		t.Fatalf("slots in use after the waits = %d, want the turn's one", slotsInUse(al))
	}
	s.release()
	if slotsInUse(al) != 0 {
		t.Fatal("slot leaked")
	}
}

// A turn cancelled while its slot is taken by another turn does not wait to
// reclaim it, and its release then gives back nothing it does not hold.
func TestTurnSlot_ReclaimCancelled(t *testing.T) {
	al := slotLoop(1)
	s := &turnSlot{al: al}
	s.acquire(context.Background())
	s.lend()
	other := &turnSlot{al: al}
	other.acquire(context.Background()) // another turn takes the lent slot
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.reclaim(ctx)
	s.release()
	if slotsInUse(al) != 1 {
		t.Fatalf("slots in use = %d, want only the other turn's", slotsInUse(al))
	}
	other.release()
	if slotsInUse(al) != 0 {
		t.Fatal("slot leaked")
	}
}

// A wait that ends after its turn released the slot takes none.
func TestTurnSlot_ReclaimAfterRelease(t *testing.T) {
	al := slotLoop(1)
	s := &turnSlot{al: al}
	s.acquire(context.Background())
	s.lend()
	s.release()
	s.reclaim(context.Background())
	if slotsInUse(al) != 0 {
		t.Fatal("a wait reclaimed a slot for a finished turn")
	}
}

// A panic in the turn still returns its slot (processSessionMessage
// releases it in a defer).
func TestTurnSlot_ReleasedOnPanic(t *testing.T) {
	al := slotLoop(1)
	s := &turnSlot{al: al}
	s.acquire(context.Background())
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the turn did not panic")
			}
		}()
		defer s.release()
		s.lend()
		defer s.reclaim(context.Background())
		panic("boom")
	}()
	if slotsInUse(al) != 0 {
		t.Fatal("slot leaked after a panic")
	}
}

// With no limit configured every operation is a no-op.
func TestTurnSlot_NoLimit(t *testing.T) {
	s := &turnSlot{al: &AgentLoop{}}
	if !s.acquire(context.Background()) {
		t.Fatal("acquire without a limit failed")
	}
	s.lend()
	s.reclaim(context.Background())
	s.release()
}
