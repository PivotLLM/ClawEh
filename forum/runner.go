// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
	"fmt"
)

// Running a controller: the run goroutine, its tracking for Close, and
// what happens when a run stops on an error.

// start runs ctrl in a goroutine whose context is the service's own,
// cancelled by Close, not the launching call's. The run is its forum's
// active run (forumEntry.active) until it has stopped and its follow-up is
// done: for a terminal status, completeTerminal; for a pause, registration
// for keep-alive; for an error, the log line and, unless the host is
// shutting down, Host.OnStuck. The lock is released and the run stops being
// the forum's active one in one step, so a caller that no longer sees the
// run can take the lock.
func (s *Service) start(store *forumStore, ctrl controller) error {
	id := store.ID()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errClosed
	}
	if f := s.forums[id]; f != nil && f.active != nil {
		s.mu.Unlock()
		return invalidState("forum %s is already running", storeRef(store))
	}
	ctx, cancel := context.WithCancel(s.ctx)
	r := &run{store: store, ctrl: ctrl, cancel: cancel, done: make(chan struct{})}
	s.withForumLocked(id, func(f *forumEntry) { f.active = r })
	s.goLocked(func() {
		defer close(r.done)
		defer cancel()
		status, err := drive(ctx, ctrl)
		if err != nil {
			status = ctrl.State().Status
			s.runFailed(store, ctrl.Snapshot(), err)
		} else {
			s.withRun(keyOf(store), func(e *runEntry) { e.stuck = false })
		}
		if status.Terminal() {
			st := ctrl.State()
			s.completeTerminal(ctx, store, ctrl.Config(), ctrl.Snapshot(), &st)
		}
		s.mu.Lock()
		store.Unlock()
		s.withForumLocked(id, func(f *forumEntry) {
			f.active = nil
			if status == StatusPaused {
				f.paused = store
			}
		})
		s.mu.Unlock()
		if status == StatusPaused {
			s.host.Logger.Infof("%s: paused", logRun(store))
		}
	})
	s.mu.Unlock()
	s.ensureKeepAlive()
	return nil
}

// goTracked runs fn on a goroutine Close waits for, unless the service is
// closed, and reports whether it started. Every goroutine the service
// starts goes through it (or goLocked).
func (s *Service) goTracked(fn func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.goLocked(fn)
}

// goLocked is goTracked for a caller that holds s.mu. The WaitGroup is
// added to under s.mu, which Close takes to set closed, so no goroutine is
// added once Close has started waiting.
func (s *Service) goLocked(fn func()) bool {
	if s.closed {
		return false
	}
	s.wg.Go(fn)
	return true
}

// drive runs ctrl until it settles. A pause or cancel request accepted
// just before Run returned (or while it was failing) leaves the forum
// pausing or cancelling with nothing to finish the transition, so while
// the state is pausing or cancelling Run is called again to settle it.
// The controller refuses requests once Run has returned (errRunEnded), so
// re-reading the state after Run returns sees every request it accepted.
// Two errors in a row stop the loop: the failure is persistent.
func drive(ctx context.Context, ctrl controller) (Status, error) {
	status, err := ctrl.Run(ctx)
	for {
		st := ctrl.State().Status
		if (st != StatusPausing && st != StatusCancelling) || st == status {
			return status, err
		}
		prev := err
		status, err = ctrl.Run(ctx)
		if err != nil && prev != nil {
			return status, err
		}
	}
}

// runFailed logs a run that stopped on an error and leaves it as it is on
// disk (it resumes with forum_resume or at the next start). A host shutdown
// is expected and logged at Info; anything else is logged at Error naming
// the forum and the run and reported once through Host.OnStuck.
func (s *Service) runFailed(store *forumStore, snap *Snapshot, err error) {
	id, ref := snap.ForumID, storeRef(store)
	name := fmt.Sprintf("forum %s run %d", ref, snap.Run)
	if errors.Is(err, ErrShuttingDown) || (s.ctx.Err() != nil && errors.Is(err, context.Canceled)) {
		s.host.Logger.Infof("%s: stopped by the shutdown; it resumes at the next start", name)
		return
	}
	s.host.Logger.Errorf("%s: stopped and is stuck until it is resumed: %v", name, err)
	s.stuckOnce(runKey{id: id, run: snap.Run}, snap.Origin, err)
}

// reportStuck reports a forum Recover could not reopen through
// Host.OnStuck, with its latest run and origin when they are readable.
func (s *Service) reportStuck(scope Scope, id string, err error) {
	key := runKey{id: id}
	origin := Origin{AgentID: scope.AgentID}
	if store, openErr := s.open(scope, id); openErr == nil {
		if n, runErr := latestRun(store); runErr == nil && n > 0 {
			key.run = n
			if snap, snapErr := store.Run(n).ReadSnapshot(); snapErr == nil {
				origin = snap.Origin
			}
		}
	}
	s.stuckOnce(key, origin, err)
}

// stuckOnce calls Host.OnStuck for a run unless it was already called in
// this process.
func (s *Service) stuckOnce(key runKey, origin Origin, err error) {
	if s.host.OnStuck == nil {
		return
	}
	var seen bool
	s.withRun(key, func(r *runEntry) {
		seen = r.stuck
		r.stuck = true
	})
	if !seen {
		s.host.OnStuck(key.id, key.run, origin, err)
	}
}
