// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"
)

// The keep-alive loop (DESIGN.md §7.10): touching the temporary agents of
// paused and running forums, and retrying pending agent deletions and
// completion notices.

// keepAliveInterval is how often the temporary agents of paused and
// running forums are touched so they outlive the registry's idle TTL, and
// how often pending temporary-agent deletions and completion notices are
// retried (DESIGN.md §7.10).
const keepAliveInterval = time.Hour

// ensureKeepAlive starts the keep-alive goroutine once.
func (s *Service) ensureKeepAlive() {
	s.keepAlive.Do(func() {
		s.goTracked(s.runKeepAlive)
	})
}

// runKeepAlive runs keepAliveTick every keepAliveEvery until the service
// closes.
func (s *Service) runKeepAlive() {
	t := time.NewTicker(s.keepAliveEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.keepAliveTick(s.ctx)
		}
	}
}

// keepAliveTick touches the temporary agents of every paused and running
// forum, then retries the pending temporary-agent deletions and completion
// notices.
func (s *Service) keepAliveTick(ctx context.Context) {
	s.mu.Lock()
	var stores []*forumStore
	for _, f := range s.forums {
		if f.paused != nil {
			stores = append(stores, f.paused)
		}
		if f.active != nil {
			stores = append(stores, f.active.store)
		}
	}
	s.mu.Unlock()
	for _, store := range stores {
		s.touchForum(ctx, store)
	}
	s.retryCleanups(ctx)
	s.retryNotices(ctx)
}

// retryCleanups retries the temporary-agent deletion of every run with a
// pending cleanup (runEntry.cleanup). A run whose forum is gone, or whose deletion succeeds, leaves
// the set; one whose forum is running or locked by another process is
// tried again at the next tick.
func (s *Service) retryCleanups(ctx context.Context) {
	pending := s.pendingRuns(func(r *runEntry) (Scope, bool) {
		if r.cleanup == nil {
			return Scope{}, false
		}
		return *r.cleanup, true
	})
	for _, key := range sortedRunKeys(pending) {
		if _, ok := s.running(pending[key], key.id); ok {
			continue
		}
		if s.retryCleanup(ctx, pending[key], key) {
			s.forgetCleanup(key)
		}
	}
}

// retryCleanup retries one run's temporary-agent deletion and reports
// whether nothing is left to do.
func (s *Service) retryCleanup(ctx context.Context, scope Scope, key runKey) bool {
	defer s.control(key.id)()
	store, err := s.open(scope, key.id)
	if errors.Is(err, ErrNotFound) {
		return true
	}
	if err != nil {
		s.host.Logger.Warnf("forum %s run %d: temporary agents: %v", s.ref(scope, key.id), key.run, err)
		return false
	}
	if err := store.Lock(); err != nil {
		return false
	}
	defer store.Unlock()
	run := store.Run(key.run)
	if err := s.deleteTempAgents(ctx, run); err != nil {
		s.host.Logger.Warnf("%s: %v (retried every %s)", logRun(run), err, s.keepAliveEvery)
		return false
	}
	// Agents the host deletes when their turns end are still listed.
	if ids, err := agentsMarker(run); err != nil || len(ids) > 0 {
		return false
	}
	s.host.Logger.Infof("%s: its remaining temporary agents are deleted", logRun(run))
	return true
}

// touchForum touches the temporary agents of one forum (those in its
// cleanupAgents marker), logging failures.
func (s *Service) touchForum(ctx context.Context, store *forumStore) {
	ids, err := agentsMarker(store)
	if err != nil {
		s.host.Logger.Warnf("%s: keep-alive: %v", logRun(store), err)
		return
	}
	for _, agentID := range ids {
		if err := s.host.Agents.Touch(ctx, store.owner, agentID); err != nil {
			s.host.Logger.Warnf("%s: keep-alive of temporary agent %s: %v", logRun(store), agentID, err)
		}
	}
}

// sortedRunKeys returns the keys of m by forum ID, then run.
func sortedRunKeys[V any](m map[runKey]V) []runKey {
	return slices.SortedFunc(maps.Keys(m), func(a, b runKey) int {
		if c := strings.Compare(a.id, b.id); c != 0 {
			return c
		}
		return a.run - b.run
	})
}
