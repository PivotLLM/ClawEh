// ClawEh
// License: MIT

package agent

import (
	"context"
	"sync"
)

// turnSlot is a turn's hold on a max_concurrent_turns slot. It travels on the
// turn's context (withTurnSlot), so a wait inside the turn can lend it out:
// an Ask waiting for the asked agent (which may need a slot to finish a turn
// queued ahead of the ask), or a request waiting for a person's answer (so a
// person taking an hour does not stall every other turn). The turn takes it
// back when the last of its waits ends. A turn may wait in several places at
// once (parallel tool calls each asking): the slot is lent by the first and
// reclaimed after the last.
//
// An asked turn (constants.AgentMessageChannel) holds none: its asker lent
// one while it waits, and with every slot held by askers the asked turn could
// never run. Such a turn's context carries no turnSlot, so its own waits lend
// nothing.
type turnSlot struct {
	al *AgentLoop

	mu   sync.Mutex
	held bool // the turn owns a semaphore slot now
	lent int  // waits in progress
	done bool // the turn is over: nothing may take a slot for it any more
}

type turnSlotKey struct{}

// withTurnSlot puts s on ctx. A nil s hides the slot of an enclosing turn
// from work that outlives it (/ask's background ask).
func withTurnSlot(ctx context.Context, s *turnSlot) context.Context {
	return context.WithValue(ctx, turnSlotKey{}, s)
}

// turnSlotFrom returns the slot on ctx, or nil.
func turnSlotFrom(ctx context.Context) *turnSlot {
	if s, ok := ctx.Value(turnSlotKey{}).(*turnSlot); ok {
		return s
	}
	return nil
}

// acquire waits for the turn's slot, or returns false when ctx ends first.
// Always true when no limit is configured.
func (s *turnSlot) acquire(ctx context.Context) bool {
	if !s.al.acquireTurnSlot(ctx) {
		return false
	}
	s.mu.Lock()
	s.held = true
	s.mu.Unlock()
	return true
}

// release gives the slot back at the end of the turn. Safe to call more than
// once, and whether or not the slot is lent out at the time.
func (s *turnSlot) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	if s.held {
		s.held = false
		s.al.releaseTurnSlot()
	}
}

// lend gives the slot up for a wait. Every lend is paired with a reclaim.
func (s *turnSlot) lend() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lent++
	if s.held {
		s.held = false
		s.al.releaseTurnSlot()
	}
}

// reclaim ends a wait. The last one takes the slot back, waiting for one to
// free; when ctx ends first the rest of the turn (which is ending anyway)
// runs without one. It waits under the lock, so two waits ending together
// cannot both take a slot, and a wait starting meanwhile (its lend blocks)
// only begins once the slot is back, then lends it straight out again.
func (s *turnSlot) reclaim(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lent--
	if s.lent == 0 && !s.held && !s.done && s.al.acquireTurnSlot(ctx) {
		s.held = true
	}
}
