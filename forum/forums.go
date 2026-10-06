// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
)

// Forums and runs (DESIGN.md §7.21). A forum is a directory under the
// caller's base holding its current configuration (forum.json) and its runs
// (runs/<n>/). The configuration is set up step by step (a template, an
// import, merge patches) without having to be valid, and can be changed
// whenever the forum is not running. Every launch validates the current
// configuration and starts a new run from the beginning; earlier runs are
// kept as they are.

// NewForum creates a forum with an empty configuration ({}) in the scope
// and returns its ID.
//
// The forum's lock is held from before its directory exists until
// forum.json is written, so Recover never removes the half-created
// directory (ListIncomplete) from under it.
func (s *Service) NewForum(_ context.Context, scope Scope) (string, error) {
	if !filepath.IsAbs(scope.BaseDirectory) {
		return "", fmt.Errorf("new forum: base directory %q is not absolute", scope.BaseDirectory)
	}
	id := uuid.NewString()
	guard := newForumHandle(scope.BaseDirectory, id)
	if err := guard.Lock(); err != nil {
		return "", err
	}
	defer guard.Unlock()
	store, err := CreateStore(scope.BaseDirectory, id)
	if err != nil {
		return "", err
	}
	raw, err := formatConfig([]byte(`{}`))
	if err != nil {
		return "", err
	}
	if err := store.WriteForumConfig(raw); err != nil {
		return "", errors.Join(err, guard.Remove())
	}
	s.host.Logger.Infof("forum %s: created by agent %s", id, scope.AgentID)
	return id, nil
}

// SetConfig replaces a forum's configuration with raw, which must be a
// JSON object (a template, or a configuration exported from a forum).
func (s *Service) SetConfig(ctx context.Context, scope Scope, id string, raw []byte) error {
	if !isJSONObject(raw) {
		return argIssue("the configuration must be a JSON object")
	}
	return s.editConfig(ctx, scope, id, func([]byte) ([]byte, error) { return raw, nil })
}

// UpdateConfig applies patch, an RFC 7386 JSON merge patch that must be a
// JSON object, to a forum's configuration.
func (s *Service) UpdateConfig(ctx context.Context, scope Scope, id string, patch []byte) error {
	if _, isObject, err := objectMembers(patch); err != nil || !isObject {
		return argIssue("the changes must be a JSON object")
	}
	return s.editConfig(ctx, scope, id, func(current []byte) ([]byte, error) {
		return mergePatch(current, patch)
	})
}

// editConfig replaces a forum's configuration with what edit makes of it,
// formatted (formatConfig). It is serialised with the forum's other control
// operations and holds the forum's lock while it writes (ErrLocked when
// another process has it). A forum whose latest run is running is refused
// (errRunning); a new, paused or ended one can be changed.
func (s *Service) editConfig(ctx context.Context, scope Scope, id string, edit func([]byte) ([]byte, error)) error {
	defer s.control(id)()
	r, err := s.live(ctx, scope, id)
	if err != nil {
		return err
	}
	if r != nil {
		return errRunning(id)
	}
	store, err := s.open(scope, id)
	if err != nil {
		return err
	}
	if err = store.Lock(); err != nil {
		return err
	}
	defer store.Unlock()
	if err = refuseBusy(store); err != nil {
		return err
	}
	current, _, err := store.ReadForumConfig()
	if err != nil {
		return err
	}
	next, err := edit(current)
	if err != nil {
		return err
	}
	formatted, err := formatConfig(next)
	if err != nil {
		return err
	}
	return store.WriteForumConfig(formatted)
}

// ExportConfig returns a forum's current configuration, indented, whatever
// the state of its runs.
func (s *Service) ExportConfig(_ context.Context, scope Scope, id string) ([]byte, error) {
	store, err := s.open(scope, id)
	if err != nil {
		return nil, err
	}
	raw, _, err := store.ReadForumConfig()
	if err != nil {
		return nil, err
	}
	return formatConfig(raw)
}

// ValidateConfig validates a forum's current configuration (Validate)
// without creating anything.
func (s *Service) ValidateConfig(ctx context.Context, id string, opts LaunchOptions) error {
	store, err := s.open(opts.Scope, id)
	if err != nil {
		return err
	}
	raw, _, err := store.ReadForumConfig()
	if err != nil {
		return err
	}
	return s.Validate(ctx, raw, opts)
}

// errRunning refuses an operation that needs a forum whose latest run is
// not running.
func errRunning(id string) error {
	return invalidState("forum %s is running; pause or cancel it first", id)
}

// errNotLaunched refuses an operation that needs a run of a forum that has
// none.
func errNotLaunched(id string) error {
	return invalidState("forum %s has not been launched", id)
}

// errNoRun refuses a run number the forum does not have.
func errNoRun(id string, run int) error {
	return invalidState("forum %s has no run %d", id, run)
}

// busy reports whether a run in status st is running: not paused and not
// ended. A forum whose latest run is busy cannot be changed, launched or
// deleted.
func busy(st Status) bool {
	return st != StatusPaused && !st.Terminal()
}

// startedRuns returns the runs of a forum whose launch got as far as its
// snapshot, in ascending order; a run directory without a snapshot is a
// launch that did not finish (or is in progress).
func startedRuns(store *Store) ([]int, error) {
	runs, err := store.Runs()
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(runs))
	for _, n := range runs {
		if store.Run(n).HasSnapshot() {
			out = append(out, n)
		}
	}
	return out, nil
}

// latestRun returns the forum's latest started run, or 0 when it has none.
func latestRun(store *Store) (int, error) {
	runs, err := startedRuns(store)
	if err != nil || len(runs) == 0 {
		return 0, err
	}
	return runs[len(runs)-1], nil
}

// refuseBusy refuses (errRunning) when the forum's latest run is busy. A
// latest run whose records are damaged never runs again and does not
// refuse.
func refuseBusy(store *Store) error {
	n, err := latestRun(store)
	if err != nil || n == 0 {
		return err
	}
	_, _, st, err := loadView(store.Run(n))
	switch {
	case errors.Is(err, ErrCorrupt):
		return nil
	case err != nil:
		return err
	case busy(st.Status):
		return errRunning(store.ID())
	}
	return nil
}

// configChanged reports whether the forum's current configuration differs
// from the one the run with snapshot snap used.
func configChanged(store *Store, snap *Snapshot) (bool, error) {
	raw, _, err := store.ReadForumConfig()
	if err != nil {
		return false, err
	}
	return configDigest(raw) != snap.ConfigDigest, nil
}

// configDigest is the digest a run records for raw (the digest of
// formatConfig(raw)), or "" when raw is not JSON.
func configDigest(raw []byte) string {
	formatted, err := formatConfig(raw)
	if err != nil {
		return ""
	}
	return digest(formatted)
}

// configName is the `name` of a configuration that may not be valid yet,
// or "" when it has none.
func configName(raw []byte) string {
	var named struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &named) != nil {
		return ""
	}
	return named.Name
}

// revertRun undoes a run whose launch did not finish: the temporary agents
// created so far are deleted (those that could not be stay in the run's
// agents marker, which the next launch of that number extends or recovery
// retries, and a failure is logged), the notice marker cleared and the
// run's directory removed, so the forum is as it was before the launch.
// The caller holds the lock. It runs even if the launching call was
// cancelled.
func (s *Service) revertRun(ctx context.Context, store *Store) error {
	if err := s.deleteTempAgents(context.WithoutCancel(ctx), store); err != nil {
		s.host.Logger.Warnf("forum %s: undoing run %d: %v (the registry's idle TTL removes them)", store.ID(), store.RunNumber(), err)
	}
	if err := store.ClearCleanup(cleanupNotice); err != nil {
		return err
	}
	return store.RemoveRun()
}

// undoUnstarted reverts every run of the forum whose launch did not get as
// far as its snapshot (revertRun) and returns the started runs. The caller
// holds the lock.
func (s *Service) undoUnstarted(ctx context.Context, store *Store) ([]int, error) {
	runs, err := store.Runs()
	if err != nil {
		return nil, err
	}
	var started []int
	for _, n := range runs {
		r := store.Run(n)
		if r.HasSnapshot() {
			started = append(started, n)
			continue
		}
		s.host.Logger.Warnf("forum %s: run %d did not start; removing it", store.ID(), n)
		if err := s.revertRun(ctx, r); err != nil {
			return nil, err
		}
	}
	return started, nil
}

// supersede ends a run that a later launch replaces: a paused run (or one
// left unfinished by a crash during such a launch) is cancelled and its
// terminal work done without a completion notice, since the launcher
// started the new run itself; a run whose records are damaged only has its
// temporary agents deleted and its notice marker cleared. The caller holds
// the lock.
func (s *Service) supersede(ctx context.Context, store *Store, damaged bool) error {
	id, n := store.ID(), store.RunNumber()
	s.forgetPaused(id)
	if damaged {
		if err := s.deleteTempAgents(ctx, store); err != nil {
			s.host.Logger.Warnf("forum %s: run %d: %v (retried every %s)", id, n, err, s.keepAliveEvery)
			s.registerCleanup(Scope{AgentID: store.owner, BaseDirectory: store.base}, store)
		}
		return store.ClearCleanup(cleanupNotice)
	}
	ctrl, err := s.openCtrl(ctx, store, s.host)
	if err != nil {
		return err
	}
	if err = ctrl.RequestCancel(); err != nil {
		return err
	}
	// The cancel is committed; a crash from here leaves a cancelling run
	// that recovery finishes, still without a notice.
	if err = store.ClearCleanup(cleanupNotice); err != nil {
		return err
	}
	status, err := drive(s.ctx, ctrl) //nolint:contextcheck // finishing the run must not depend on the launching call
	if err != nil {
		return err
	}
	if status.Terminal() {
		st := ctrl.State()
		s.completeTerminal(ctx, store, ctrl.Config(), ctrl.Snapshot(), &st)
	}
	s.host.Logger.Infof("forum %s: run %d superseded by a new run: %s", id, n, status)
	return nil
}

// formatConfig indents a configuration document for forum.json and export.
// Formatting a formatted document changes nothing.
func formatConfig(raw []byte) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Indent(&b, bytes.TrimSpace(raw), "", "  "); err != nil {
		return nil, fmt.Errorf("format configuration: %w", err)
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}
