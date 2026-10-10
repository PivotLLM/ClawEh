// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// The work that follows a run's terminal state (DESIGN.md §5.11):
// result.json, deleting the temporary agents and the completion notice,
// each retried until done.

// cleanupNotice is the marker (<base>/.cleanup/<uuid>.<run>.notice) of a
// run's completion notice not yet delivered. It is written at launch and
// cleared once the launcher has been notified, so a restart between the
// terminal commit and the notice still delivers it.
const cleanupNotice = "notice"

// completeTerminal does the work that follows a terminal state, each step
// only if still pending, so a restart can repeat it (DESIGN.md §5.11):
//
//  1. result.json is written if the controller did not get to it;
//  2. the temporary agents are deleted (deleteTempAgents); a failure is
//     retried by the keep-alive loop until it succeeds;
//  3. the launcher is notified with result.json, only once it is
//     committed, and the notice marker is cleared (notify, on a goroutine
//     of its own so a blocking Notifier never holds the forum).
//
// A result.json that cannot be written, or a notice that fails, keeps the
// notice marker and is retried by the keep-alive loop (noticeFailed).
//
// The store must be locked by the caller. Failures are logged.
func (s *Service) completeTerminal(ctx context.Context, store *forumStore, cfg *Config, snap *Snapshot, st *State) {
	ctx = context.WithoutCancel(ctx)
	res, resErr := store.ReadResult()
	if errors.Is(resErr, ErrNotFound) {
		res = buildResult(cfg, snap, st)
		resErr = store.WriteResult(res)
	}
	if err := s.deleteTempAgents(ctx, store); err != nil {
		s.host.Logger.Warnf("%s: %v (retried every %s)", logRun(store), err, s.keepAliveEvery)
		s.registerCleanup(Scope{AgentID: store.owner, BaseDirectory: store.base}, store)
	}
	if resErr != nil {
		s.noticeFailed(store, fmt.Errorf("writing %s: %w", fileResult, resErr))
		return
	}
	_, pending, err := store.Cleanup(cleanupNotice)
	switch {
	case err != nil:
		s.noticeFailed(store, err)
	case pending:
		s.notify(store, snap.Origin, res)
	default:
		s.forgetNotice(keyOf(store))
	}
}

// notify delivers the completion notice on a goroutine of its own and
// clears the notice marker afterwards. A notice that fails because the
// service is closing keeps its marker, so the next start delivers it; any
// other failure keeps it too and is retried (noticeFailed). A notice
// already in flight for the run is not sent again.
func (s *Service) notify(store *forumStore, origin Origin, res *Result) {
	key := keyOf(store)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runStateLocked(key).notifying {
		return
	}
	started := s.goLocked(func() {
		defer s.withRun(key, func(r *runEntry) { r.notifying = false })
		if err := s.host.Notifier.ForumFinished(s.ctx, origin, s.launchChat(key), res); err != nil {
			if s.ctx.Err() != nil {
				s.host.Logger.Infof("%s: notifying agent %s was cut by the shutdown; it is notified at the next start", logRun(store), origin.AgentID)
				return
			}
			s.noticeFailed(store, fmt.Errorf("notifying agent %s: %w", origin.AgentID, err))
			return
		}
		s.host.Logger.Infof("%s: %s; agent %s notified", logRun(store), res.Status, origin.AgentID)
		s.forgetNotice(key)
		s.takeLaunchChat(key)
		if err := store.ClearCleanup(cleanupNotice); err != nil {
			s.host.Logger.Warnf("%s: notice marker: %v", logRun(store), err)
		}
	})
	if started {
		s.withRunLocked(key, func(r *runEntry) { r.notifying = true })
	} // else closed: nothing is sent; the marker stays for the next start
}

// maxNoticeTries is how many times this process tries a run's completion
// notice (and the result.json it waits for) before giving it up: the first
// try and the keep-alive loop's retries.
const maxNoticeTries = 5

// noticeRetry is a run whose completion notice failed, for the keep-alive
// loop to retry; the zero value is none. It is a value, so a copy of a
// runEntry (runState) shares nothing with the record.
type noticeRetry struct {
	scope Scope
	tries int
}

// noticeFailed records a failed try of a run's completion notice (err is
// why: result.json could not be written, or the Notifier failed). The
// notice marker is kept and the keep-alive loop tries again; after
// maxNoticeTries the notice is given up with a warning and its marker
// cleared, so no later start tries it again.
func (s *Service) noticeFailed(store *forumStore, err error) {
	key := keyOf(store)
	var tries int
	s.withRun(key, func(r *runEntry) {
		if r.notice.tries == 0 {
			r.notice.scope = Scope{AgentID: store.owner, BaseDirectory: store.base}
		}
		r.notice.tries++
		tries = r.notice.tries
		if tries >= maxNoticeTries {
			r.notice = noticeRetry{}
		}
	})
	if tries < maxNoticeTries {
		s.host.Logger.Warnf("%s: completion notice: %v (try %d of %d; retried every %s)", logRun(store), err, tries, maxNoticeTries, s.keepAliveEvery)
		s.ensureKeepAlive()
		return
	}
	s.host.Logger.Warnf("%s: completion notice given up after %d tries: %v", logRun(store), tries, err)
	s.takeLaunchChat(key)
	if clearErr := store.ClearCleanup(cleanupNotice); clearErr != nil {
		s.host.Logger.Warnf("%s: notice marker: %v", logRun(store), clearErr)
	}
}

// retryNotices retries the completion notice of every run with a failed
// notice (runEntry.notice) that is not being delivered now: the run is
// opened and locked, and completeTerminal writes result.json if it is still missing and notifies
// if the notice is still pending. A run whose forum is gone leaves the
// set; one whose forum is running or locked is tried at the next tick.
func (s *Service) retryNotices(ctx context.Context) {
	pending := s.pendingRuns(func(r *runEntry) (Scope, bool) {
		if r.notice.tries == 0 || r.notifying {
			return Scope{}, false
		}
		return r.notice.scope, true
	})
	for _, key := range sortedRunKeys(pending) {
		if _, ok := s.running(pending[key], key.id); ok {
			continue
		}
		s.retryNotice(ctx, pending[key], key)
	}
}

// retryNotice is retryNotices for one run.
func (s *Service) retryNotice(ctx context.Context, scope Scope, key runKey) {
	defer s.control(key.id)()
	store, err := s.open(scope, key.id)
	if errors.Is(err, ErrNotFound) {
		s.forgetNotice(key)
		return
	}
	if err != nil {
		s.host.Logger.Warnf("forum %s run %d: completion notice: %v", s.ref(scope, key.id), key.run, err)
		return
	}
	if err = store.Lock(); err != nil {
		return
	}
	defer store.Unlock()
	run := store.Run(key.run)
	cfg, snap, st, err := load(run)
	if err != nil {
		s.noticeFailed(run, err)
		return
	}
	s.completeTerminal(ctx, run, cfg, snap, st)
}

// agentsMarker reads the cleanupAgents marker: the temporary agents still
// to be deleted (nil when the marker is absent).
func agentsMarker(store *forumStore) ([]string, error) {
	data, ok, err := store.Cleanup(cleanupAgents)
	if err != nil || !ok {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, fmt.Errorf("%w: forum %s: cleanup marker %s: %w", ErrCorrupt, store.ID(), cleanupAgents, err)
	}
	return ids, nil
}

// setAgentsMarker writes the cleanupAgents marker, or clears it when ids
// is empty.
func setAgentsMarker(store *forumStore, ids []string) error {
	if len(ids) == 0 {
		return store.ClearCleanup(cleanupAgents)
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return store.SetCleanup(cleanupAgents, data)
}

// agentsLeftError is deleteTempAgents' failure: the temporary agents of a
// forum that could not be deleted (they stay in the marker for the next
// attempt).
type agentsLeftError struct {
	forumID string
	ref     string // the forum as messages name it (Ref)
	agents  []string
	err     error
}

func (e *agentsLeftError) Error() string {
	return fmt.Sprintf("forum %s: temporary agents %v could not be deleted: %v", e.forumID, e.agents, e.err)
}

func (e *agentsLeftError) Unwrap() error { return e.err }

// deleteTempAgents deletes every agent in the forum's cleanupAgents
// marker through Agents.Delete. The marker, written at launch as each
// agent is created, is the record of what is still to delete: once it is
// gone there is nothing to do, so repeating the call (or a restart) never
// deletes twice. Delete failing with ErrNotFound counts as done. Agents
// that could not be deleted stay in the marker for the next attempt (the
// keep-alive loop retries a terminal forum's, the registry's TTL is the
// backstop); the error is an *agentsLeftError naming them.
func (s *Service) deleteTempAgents(ctx context.Context, store *forumStore) error {
	ids, err := agentsMarker(store)
	if err != nil || len(ids) == 0 {
		return err
	}
	var remaining, pending []string
	var failures error
	for _, agentID := range ids {
		err := s.host.Agents.Delete(ctx, store.owner, agentID)
		switch {
		case err == nil || errors.Is(err, ErrNotFound):
			continue
		case errors.Is(err, ErrDeletePending):
			pending = append(pending, agentID)
			continue
		}
		remaining = append(remaining, agentID)
		failures = errors.Join(failures, fmt.Errorf("agent %s: %w", agentID, err))
	}
	// An agent the host deletes when its turn ends stays in the marker, and
	// the run is retried like a failed deletion, so a restart before the
	// turn ends still deletes it; it is not reported as a failure.
	keep := make([]string, 0, len(remaining)+len(pending))
	keep = append(append(keep, remaining...), pending...)
	if err := setAgentsMarker(store, keep); err != nil {
		return errors.Join(failures, err)
	}
	if failures != nil {
		return &agentsLeftError{forumID: store.ID(), ref: storeRef(store), agents: remaining, err: failures}
	}
	if len(pending) > 0 {
		s.host.Logger.Infof("%s: temporary agents %v are deleted when their turns end", logRun(store), pending)
		s.registerCleanup(Scope{AgentID: store.owner, BaseDirectory: store.base}, store)
		return nil
	}
	s.host.Logger.Debugf("%s: deleted temporary agents %v", logRun(store), ids)
	return nil
}
