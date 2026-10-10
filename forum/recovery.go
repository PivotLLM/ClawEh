// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Restart recovery (DESIGN.md §7.18): what Service.Recover does with the
// forums a restart interrupted.

// Recover is called once at startup with every agent's scope, so the
// forums interrupted by a restart continue (DESIGN.md §7.18). Per scope it
// first removes roots staged for deletion (listStaged, removeStaged), then
// applies recoverOne to every forum in listForums. Errors are logged per
// forum and do not stop the scan; a forum that cannot be reopened (other
// than one locked by another process) is also reported through
// Host.OnStuck. The returned error is the first one, for the caller's log
// line.
func (s *Service) Recover(ctx context.Context, scopes []Scope) error {
	var first error
	note := func(err error) {
		if first == nil {
			first = err
		}
	}
	for _, scope := range scopes {
		staged, err := listStaged(scope.BaseDirectory)
		if err != nil {
			s.host.Logger.Errorf("forum recovery in %s: %v", scope.BaseDirectory, err)
			note(err)
		}
		for _, id := range staged {
			if rmErr := removeStaged(scope.BaseDirectory, id); rmErr != nil {
				s.host.Logger.Errorf("forum %s: finishing its removal: %v", id, rmErr)
				note(fmt.Errorf("forum %s: %w", id, rmErr))
			}
		}
		s.removeIncomplete(scope)
		ids, err := listForums(scope.BaseDirectory)
		if err != nil {
			s.host.Logger.Errorf("forum recovery in %s: %v", scope.BaseDirectory, err)
			note(err)
			continue
		}
		for _, id := range ids {
			if err := s.recoverOne(ctx, scope, id); err != nil {
				s.host.Logger.Errorf("forum %s: recover: %v", s.ref(scope, id), err)
				note(fmt.Errorf("forum %s: %w", id, err))
				if !errors.Is(err, ErrLocked) && !errors.Is(err, errClosed) {
					s.reportStuck(scope, id, err)
				}
			}
		}
	}
	return first
}

// removeIncomplete removes the directories of forums whose creation died
// before forum.json was written (listIncomplete). One locked by a forum
// being created right now is left alone.
func (s *Service) removeIncomplete(scope Scope) {
	ids, err := listIncomplete(scope.BaseDirectory)
	if err != nil {
		s.host.Logger.Warnf("forum recovery in %s: %v", scope.BaseDirectory, err)
		return
	}
	for _, id := range ids {
		switch err := newForumHandle(scope.BaseDirectory, id).Remove(); {
		case errors.Is(err, ErrLocked):
		case err != nil:
			s.host.Logger.Warnf("forum %s: removing an incomplete forum: %v", id, err)
		default:
			s.host.Logger.Infof("forum %s: removed a forum whose creation did not finish", id)
		}
	}
}

// recoverOne applies Recover's rules to one forum (DESIGN.md §7.18):
//
//   - every run whose launch did not get as far as its snapshot is undone
//     (revertRun), so the forum is as it was before that launch, and the
//     temporary agents such undone runs left are deleted;
//   - an earlier run with work pending (finishEarlier) gets it done;
//   - the latest run: a terminal one gets its unfinished terminal work
//     done (completeTerminal: result.json, agent deletion, notice); a
//     paused one stays paused, its agents are touched once and it is
//     registered for keep-alive; a queued or running one is resumed
//     (queued gets CommitLaunched); a pausing or cancelling one is resumed
//     without a commit, so the controller completes the pending pause or
//     cancel.
func (s *Service) recoverOne(ctx context.Context, scope Scope, id string) error {
	if _, ok := s.running(scope, id); ok {
		return nil
	}
	store, err := s.open(scope, id)
	if err != nil {
		return err
	}
	if err = store.Lock(); err != nil {
		return err
	}
	started, err := s.undoUnstarted(ctx, store)
	if err != nil {
		store.Unlock()
		return err
	}
	s.deleteLeftAgents(ctx, store, started)
	if len(started) == 0 {
		store.Unlock()
		return nil
	}
	for _, n := range started[:len(started)-1] {
		s.finishEarlier(ctx, store.Run(n))
	}
	run := store.Run(started[len(started)-1])
	cfg, snap, st, err := load(run)
	if err != nil {
		store.Unlock()
		return err
	}
	switch st.Status {
	case StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
		defer store.Unlock()
		s.completeTerminal(ctx, run, cfg, snap, st)
		return nil
	case StatusPaused:
		store.Unlock()
		s.touchForum(ctx, run)
		s.registerPaused(run)
		return nil
	case StatusQueued:
		if err := markLaunched(run, st); err != nil {
			store.Unlock()
			return err
		}
	case StatusNew, StatusRunning, StatusPausing, StatusCancelling:
	}
	s.host.Logger.Infof("%s: resuming after restart (%s)", logRun(run), st.Status)
	return s.openAndStart(ctx, run, nil)
}

// deleteLeftAgents deletes the temporary agents left in the agents marker
// of a run that no longer exists (a launch undone whose agents could not
// all be deleted). Failures are logged; the marker keeps them.
func (s *Service) deleteLeftAgents(ctx context.Context, store *forumStore, started []int) {
	marked, err := store.MarkedRuns(cleanupAgents)
	if err != nil {
		s.host.Logger.Warnf("%s: %v", logForum(store), err)
		return
	}
	for _, n := range marked {
		if slices.Contains(started, n) {
			continue
		}
		if err := s.deleteTempAgents(ctx, store.Run(n)); err != nil {
			s.host.Logger.Warnf("%s: run %d that did not start: %v", logForum(store), n, err)
		}
	}
}

// finishEarlier finishes the pending work of a run that is not the
// forum's latest: a terminal run with markers gets its terminal work done
// (completeTerminal); a run a crash left unfinished while a later run was
// being launched, markers or not, is superseded (cancelled without a
// notice), as that launch would have done. Failures are logged. The caller
// holds the lock.
func (s *Service) finishEarlier(ctx context.Context, run *forumStore) {
	_, agents, agentsErr := run.Cleanup(cleanupAgents)
	_, notice, noticeErr := run.Cleanup(cleanupNotice)
	if err := errors.Join(agentsErr, noticeErr); err != nil {
		s.host.Logger.Warnf("%s: %v", logRun(run), err)
		return
	}
	if !agents && !notice {
		// Nothing pending is recorded, but a supersede cut short after the
		// cancel cleared the notice leaves the run cancelling: the state
		// cache (loadView) says so without verifying every file.
		_, _, st, err := loadView(run)
		if err != nil || st.Status.Terminal() {
			return
		}
	}
	cfg, snap, st, err := load(run)
	switch {
	case errors.Is(err, ErrCorrupt):
		err = s.supersede(ctx, run, true)
	case err != nil:
	case st.Status.Terminal():
		s.completeTerminal(ctx, run, cfg, snap, st)
	default:
		err = s.supersede(ctx, run, false)
	}
	if err != nil {
		s.host.Logger.Warnf("%s: finishing it: %v", logRun(run), err)
	}
}
