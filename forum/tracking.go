// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"sync"
)

// The service's in-memory state. Everything the service remembers about a
// forum in this process is one forumEntry in Service.forums, and about one
// of its runs one runEntry in it, both under Service.mu. Every change goes
// through withForumLocked or withRunLocked, which drop a record as soon as
// it holds nothing, so the map never outgrows the forums in use and
// forgetting a forum is dropping its record (forgetForum).

// runKey names one run of one forum.
type runKey struct {
	id  string
	run int
}

// keyOf is the runKey of a run's store.
func keyOf(store *forumStore) runKey { return runKey{id: store.ID(), run: store.RunNumber()} }

// run is one active controller with its run's store and the forum's lock.
// At most one run of a forum is active. It stays its forum's active run
// until the run goroutine has finished the forum's terminal work or
// registered its pause and released the lock; done is closed after that.
type run struct {
	store  *forumStore
	ctrl   controller
	cancel context.CancelFunc
	done   chan struct{}
}

// forumEntry is what the service keeps about one forum ID.
type forumEntry struct {
	// active is the run whose controller is live in this process.
	active *run
	// paused is the store of the forum's paused run, whose temporary
	// agents the keep-alive loop touches (an active run's are touched
	// through active).
	paused *forumStore
	// control serialises the control operations (launch, configuration
	// edits, pause, resume, cancel, delete, retries) of the forum;
	// controlRefs counts the operations holding or waiting for it, and
	// keeps the record (and so this mutex) alive while there are any.
	control     sync.Mutex
	controlRefs int
	// runs holds the state of the forum's runs that have any.
	runs map[int]*runEntry
}

// empty reports whether the record holds nothing and can be dropped.
func (f *forumEntry) empty() bool {
	return f.active == nil && f.paused == nil && f.controlRefs == 0 && len(f.runs) == 0
}

// runEntry is what the service keeps about one run. The zero value holds
// nothing.
type runEntry struct {
	// cleanup is set while the run's temporary agents could not all be
	// deleted; the keep-alive loop retries them in this scope.
	cleanup *Scope
	// notice is set (tries > 0) while the run's completion notice (or
	// the result.json it waits for) has failed; the keep-alive loop
	// retries it (noticeFailed).
	notice noticeRetry
	// notifying is set while the completion notice is being delivered, so
	// a notice is never sent twice concurrently. It belongs to the notify
	// goroutine, which clears it, and so survives forgetForum.
	notifying bool
	// stuck is set once Host.OnStuck was called for the run in this
	// process, so it is called once; a run that later stops cleanly
	// clears it.
	stuck bool
	// launchChat is the chat the run was launched from, as the launching
	// tool call reported it. It is kept in memory only: the origin recorded
	// in snapshot.json lives in the launcher's workspace and is not
	// trusted, so a run whose launch this process did not see (one resumed
	// after a restart) has none.
	launchChat Chat
}

// withForumLocked applies fn to id's record, created if missing, and drops
// the record if fn leaves it empty. s.mu is held.
func (s *Service) withForumLocked(id string, fn func(f *forumEntry)) {
	f := s.forums[id]
	if f == nil {
		f = &forumEntry{}
		s.forums[id] = f
	}
	fn(f)
	if f.empty() {
		delete(s.forums, id)
	}
}

// withRunLocked applies fn to key's run record, created if missing, and
// drops the run's and then the forum's record if fn leaves them empty.
// s.mu is held.
func (s *Service) withRunLocked(key runKey, fn func(r *runEntry)) {
	s.withForumLocked(key.id, func(f *forumEntry) {
		r := f.runs[key.run]
		if r == nil {
			r = &runEntry{}
			if f.runs == nil {
				f.runs = map[int]*runEntry{}
			}
			f.runs[key.run] = r
		}
		fn(r)
		if *r == (runEntry{}) {
			delete(f.runs, key.run)
		}
	})
}

// withRun is withRunLocked taking s.mu.
func (s *Service) withRun(key runKey, fn func(r *runEntry)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.withRunLocked(key, fn)
}

// runState returns a copy of key's run record (the zero value when it has
// none).
func (s *Service) runState(key runKey) runEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runStateLocked(key)
}

// runStateLocked is runState for a caller that holds s.mu.
func (s *Service) runStateLocked(key runKey) runEntry {
	if f := s.forums[key.id]; f != nil {
		if r := f.runs[key.run]; r != nil {
			return *r
		}
	}
	return runEntry{}
}

// pendingRuns returns the scope of every run for which pick reports work,
// keyed by run, for a retry loop to work through without holding s.mu.
func (s *Service) pendingRuns(pick func(r *runEntry) (Scope, bool)) map[runKey]Scope {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[runKey]Scope{}
	for id, f := range s.forums {
		for n, r := range f.runs {
			if scope, ok := pick(r); ok {
				out[runKey{id: id, run: n}] = scope
			}
		}
	}
	return out
}

// control locks the control operations of one forum ID and returns the
// unlock function.
func (s *Service) control(id string) func() {
	s.mu.Lock()
	var f *forumEntry
	s.withForumLocked(id, func(e *forumEntry) {
		e.controlRefs++
		f = e
	})
	s.mu.Unlock()
	f.control.Lock()
	return func() {
		f.control.Unlock()
		s.mu.Lock()
		s.withForumLocked(id, func(e *forumEntry) { e.controlRefs-- })
		s.mu.Unlock()
	}
}

// forgetForum drops what the service keeps about a deleted forum: it is no
// longer kept alive, and none of its runs is retried, notified to the
// launching chat or remembered as stuck. A notice still being delivered
// keeps its notifying mark until its goroutine ends. The record itself
// stays only while a control operation (the Delete calling this) holds it.
func (s *Service) forgetForum(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.withForumLocked(id, func(f *forumEntry) {
		f.paused = nil
		for n, r := range f.runs {
			if !r.notifying {
				delete(f.runs, n)
				continue
			}
			*r = runEntry{notifying: true}
		}
	})
}

// registerPaused adds a paused forum to the keep-alive set and starts the
// keep-alive loop if needed.
func (s *Service) registerPaused(store *forumStore) {
	s.mu.Lock()
	s.withForumLocked(store.ID(), func(f *forumEntry) { f.paused = store })
	s.mu.Unlock()
	s.ensureKeepAlive()
}

// forgetPaused drops a forum from the keep-alive set.
func (s *Service) forgetPaused(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.withForumLocked(id, func(f *forumEntry) { f.paused = nil })
}

// registerCleanup records a run whose temporary agents are still to
// delete, for the keep-alive loop to retry.
func (s *Service) registerCleanup(scope Scope, store *forumStore) {
	s.withRun(keyOf(store), func(r *runEntry) { r.cleanup = &scope })
	s.ensureKeepAlive()
}

// forgetCleanup drops a run from the cleanup retries.
func (s *Service) forgetCleanup(key runKey) {
	s.withRun(key, func(r *runEntry) { r.cleanup = nil })
}

// forgetNotice drops a run from the notice retries.
func (s *Service) forgetNotice(key runKey) {
	s.withRun(key, func(r *runEntry) { r.notice = noticeRetry{} })
}

// setLaunchChat records the chat run key was launched from.
func (s *Service) setLaunchChat(key runKey, chat Chat) {
	if chat == (Chat{}) {
		return
	}
	s.withRun(key, func(r *runEntry) { r.launchChat = chat })
}

// launchChat returns the chat run key was launched from, or the zero Chat
// when this process did not see the launch.
func (s *Service) launchChat(key runKey) Chat {
	return s.runState(key).launchChat
}

// takeLaunchChat forgets the chat run key was launched from.
func (s *Service) takeLaunchChat(key runKey) {
	s.withRun(key, func(r *runEntry) { r.launchChat = Chat{} })
}
