// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
)

// Controlling a forum's latest run (forum_pause, forum_resume,
// forum_cancel) and deleting a forum (forum_delete). Each operation is
// serialised per forum ID (Service.control) and either acts on the live
// controller or takes the forum over from disk (takeOver).

// Pause asks a forum's latest run to pause and returns at once; the status becomes
// paused when in-flight turns finish (forumController.RequestPause). Repeating
// it is harmless: a pausing or paused forum is left as it is. A running
// forum with no live controller (interrupted) is opened, paused and run to
// its pause. Cancellation dominates: a cancelling or terminal forum is
// ErrInvalidState. ErrLocked when another process holds the forum.
func (s *Service) Pause(ctx context.Context, scope Scope, id string) error {
	defer s.control(id)()
	r, err := s.live(ctx, scope, id)
	if err != nil {
		return err
	}
	if r != nil {
		switch st := r.ctrl.State().Status; st {
		case StatusRunning, StatusQueued:
			reqErr := r.ctrl.RequestPause()
			if !errors.Is(reqErr, errRunEnded) {
				return reqErr
			}
			// The run stopped under us: wait for it to be released and
			// take the forum over.
			if err = waitDone(ctx, r); err != nil {
				return err
			}
		case StatusPausing, StatusPaused:
			return nil
		case StatusNew, StatusCancelling, StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
			return refusePause(s.ref(scope, id), st)
		}
	}
	store, _, st, err := s.takeOver(scope, id)
	if err != nil {
		return err
	}
	switch st.Status {
	case StatusPaused:
		store.Unlock()
		return nil
	case StatusPausing:
		return s.openAndStart(ctx, store, nil) // the controller completes the pause
	case StatusQueued, StatusRunning:
		if err := markLaunched(store, st); err != nil {
			store.Unlock()
			return err
		}
		return s.openAndStart(ctx, store, controller.RequestPause)
	case StatusNew, StatusCancelling, StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
	}
	store.Unlock()
	return refusePause(storeRef(store), st.Status)
}

func refusePause(ref string, st Status) error {
	if st == StatusCancelling {
		return invalidState("forum %s is being cancelled and cannot be paused", ref)
	}
	return invalidState("forum %s is %s and cannot be paused", ref, st)
}

// Resume restarts a forum's paused latest run, or a queued, running or
// pausing one with no live controller in this process (interrupted). A
// paused run whose forum's configuration has changed since it was launched
// is refused: the change takes effect in a new run (Launch). It commits
// CommitResumed when the status was paused or pausing (resume clears a
// pending pause) and CommitLaunched when it was queued, then Opens and
// starts the run. Repeating it on a running forum is a no-op. It checks
// cancellation first: a cancelling or terminal forum is ErrInvalidState,
// as is one still pausing in this process. ErrLocked when another process
// holds the forum.
func (s *Service) Resume(ctx context.Context, scope Scope, id string) error {
	defer s.control(id)()
	r, err := s.live(ctx, scope, id)
	if err != nil {
		return err
	}
	if r != nil {
		switch st := r.ctrl.State().Status; st {
		case StatusRunning, StatusQueued:
			return nil
		case StatusPausing:
			return invalidState("forum %s is still pausing; resume it once it is paused", s.ref(scope, id))
		case StatusNew, StatusPaused, StatusCancelling, StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
			return refuseResume(s.ref(scope, id), st)
		}
	}
	store, _, st, err := s.takeOver(scope, id)
	if err != nil {
		return err
	}
	if st.Status == StatusPaused {
		changed, changeErr := configChanged(store, nil)
		if changeErr != nil {
			store.Unlock()
			return changeErr
		}
		if changed {
			store.Unlock()
			return invalidState("forum %s: the configuration changed; launch to start a new run", storeRef(store))
		}
	}
	return s.resume(ctx, store, st)
}

func refuseResume(ref string, st Status) error {
	if st == StatusCancelling {
		return invalidState("forum %s is being cancelled and cannot be resumed", ref)
	}
	return invalidState("forum %s is %s and cannot be resumed", ref, st)
}

// resume is Resume after the store is locked and its state loaded. It
// consumes the lock: it is held by the started run or released.
func (s *Service) resume(ctx context.Context, store *forumStore, st *State) error {
	var commit CommitKind
	switch st.Status {
	case StatusPaused, StatusPausing:
		commit = CommitResumed
	case StatusQueued:
		commit = CommitLaunched
	case StatusRunning:
	case StatusNew, StatusCancelling, StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
		store.Unlock()
		return refuseResume(storeRef(store), st.Status)
	}
	if commit != "" {
		if err := store.AppendCommit(st.Seq+1, &Commit{Kind: commit}); err != nil {
			store.Unlock()
			return err
		}
	}
	s.forgetPaused(store.ID())
	return s.openAndStart(ctx, store, nil)
}

// Cancel stops a forum's latest run and finalises it as cancelled, keeping its partial
// work: a live one through forumController.RequestCancel; a paused or
// interrupted one by opening it, committing the cancel request and
// running it to its cancelled end. Cancelled is terminal: result.json is
// written, temporary agents deleted, the launcher notified. Repeating it
// is harmless (a cancelling or cancelled forum is left as it is); another
// terminal status is ErrInvalidState. ErrLocked when another process holds
// the forum.
func (s *Service) Cancel(ctx context.Context, scope Scope, id string) error {
	defer s.control(id)()
	r, err := s.live(ctx, scope, id)
	if err != nil {
		return err
	}
	if r != nil {
		switch st := r.ctrl.State().Status; st {
		case StatusCancelling, StatusCancelled:
			return nil
		case StatusNew, StatusCompleted, StatusIncomplete, StatusFailed:
			return invalidState("forum %s is already %s", s.ref(scope, id), st)
		case StatusQueued, StatusRunning, StatusPausing, StatusPaused:
			reqErr := r.ctrl.RequestCancel()
			if !errors.Is(reqErr, errRunEnded) {
				return reqErr
			}
			if err = waitDone(ctx, r); err != nil {
				return err
			}
		}
	}
	store, _, st, err := s.takeOver(scope, id)
	if err != nil {
		return err
	}
	switch st.Status {
	case StatusCancelled:
		store.Unlock()
		return nil
	case StatusNew, StatusCompleted, StatusIncomplete, StatusFailed:
		store.Unlock()
		return invalidState("forum %s is already %s", storeRef(store), st.Status)
	case StatusCancelling:
		return s.openAndStart(ctx, store, nil) // the controller completes the cancel
	case StatusQueued, StatusRunning, StatusPausing, StatusPaused:
	}
	s.forgetPaused(id)
	return s.openAndStart(ctx, store, controller.RequestCancel)
}

// Delete removes a forum and every run of it, unless its latest run is
// running: it takes the forum's lock without waiting (ErrLocked when
// another holder has it), rechecks the latest run's status, deletes the
// temporary agents of every run (deleteTempAgents) and then the directory
// (forumStore.Remove). A forum whose records are damaged can be deleted too.
// A running forum is refused (errBusy); a forum whose agents could not
// all be deleted is kept and the error returned, so a retry finishes the
// job. A forum ID that does not exist is ErrNotFound.
func (s *Service) Delete(ctx context.Context, scope Scope, id string) error {
	defer s.control(id)()
	r, err := s.live(ctx, scope, id)
	if err != nil {
		return err
	}
	if r != nil {
		return errBusy(s.ref(scope, id), r.ctrl.State().Status)
	}
	store, err := s.open(scope, id)
	switch {
	case errors.Is(err, errForeign):
		return err
	case errors.Is(err, ErrCorrupt) && validForumID(id):
		// A directory too damaged to open as a forum can never run; it
		// is still refused when its owner record names another agent.
		store = newForumHandle(scope.BaseDirectory, id)
		store.owner = scope.AgentID
		if ownerErr := checkForumOwner(store); errors.Is(ownerErr, errForeign) {
			return ownerErr
		}
	case err != nil:
		return err
	}
	if err = store.Lock(); err != nil {
		return err
	}
	if _, runsErr := store.Runs(); runsErr == nil {
		if err = refuseBusy(store); err != nil {
			store.Unlock()
			return err
		}
	}
	marked, err := store.MarkedRuns(cleanupAgents)
	if err != nil {
		store.Unlock()
		return err
	}
	for _, n := range marked {
		if err := s.deleteTempAgents(ctx, store.Run(n)); err != nil {
			store.Unlock()
			return err
		}
	}
	s.forgetPaused(id)
	s.forgetRuns(id)
	name := logForum(store)
	if err := store.Remove(); err != nil {
		return err
	}
	s.host.Logger.Infof("%s: deleted", name)
	return nil
}

// takeOver opens a forum that has no live controller in this process,
// takes its lock without waiting and loads its latest run's snapshot and
// state; a forum with no run is refused. The caller owns the lock on
// success; the returned store is the latest run's.
func (s *Service) takeOver(scope Scope, id string) (*forumStore, *Snapshot, *State, error) {
	store, err := s.open(scope, id)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = store.Lock(); err != nil {
		return nil, nil, nil, err
	}
	n, err := latestRun(store)
	if err == nil && n == 0 {
		err = errNotLaunched(storeRef(store))
	}
	if err != nil {
		store.Unlock()
		return nil, nil, nil, err
	}
	run := store.Run(n)
	_, snap, st, err := load(run)
	if err != nil {
		store.Unlock()
		return nil, nil, nil, err
	}
	return run, snap, st, nil
}

// markLaunched appends the CommitLaunched a launch did not get to write
// (status queued after a crash); any other status needs nothing.
func markLaunched(store *forumStore, st *State) error {
	if st.Status != StatusQueued {
		return nil
	}
	return store.AppendCommit(st.Seq+1, &Commit{Kind: CommitLaunched})
}

// openAndStart opens a locked store, applies before to the controller (a
// pause or cancel request), and starts the run. It consumes the lock: on
// failure the lock is released.
func (s *Service) openAndStart(ctx context.Context, store *forumStore, before func(controller) error) error {
	if s.isClosed() {
		store.Unlock()
		return errClosed
	}
	ctrl, err := s.openCtrl(ctx, store, s.host)
	if err != nil {
		store.Unlock()
		return err
	}
	if before != nil {
		if err := before(ctrl); err != nil {
			store.Unlock()
			return err
		}
	}
	if err := s.start(store, ctrl); err != nil { //nolint:contextcheck // the run's context is the service's
		store.Unlock()
		return err
	}
	return nil
}
