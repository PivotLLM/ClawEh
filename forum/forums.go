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
	"math"
	"path/filepath"
	"strconv"
	"time"

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
	// The owner first: a forum.json is never without one (ListIncomplete).
	if err := store.WriteForumMeta(&ForumMeta{Owner: scope.AgentID, CreatedAt: time.Now().UTC()}); err != nil {
		return "", errors.Join(err, guard.Remove())
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
// (errBusy); a new, paused or ended one can be changed.
func (s *Service) editConfig(ctx context.Context, scope Scope, id string, edit func([]byte) ([]byte, error)) error {
	defer s.control(id)()
	r, err := s.live(ctx, scope, id)
	if err != nil {
		return err
	}
	if r != nil {
		return errBusy(s.ref(scope, id), r.ctrl.State().Status)
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

// errBusy refuses an operation that needs a forum whose latest run is not
// running; st is that run's status. ref names the forum (Ref), as in every
// refusal below.
func errBusy(ref string, st Status) error {
	switch st {
	case StatusPausing:
		return invalidState("forum %s is still pausing; try again once it is paused", ref)
	case StatusCancelling:
		return invalidState("forum %s is being cancelled", ref)
	case StatusNew, StatusQueued, StatusRunning, StatusPaused, StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
	}
	return invalidState("forum %s is running; pause or cancel it first", ref)
}

// errNotLaunched refuses an operation that needs a run of a forum that has
// none.
func errNotLaunched(ref string) error {
	return invalidState("forum %s has not been launched", ref)
}

// errNoRun refuses a run number the forum does not have.
func errNoRun(ref string, run int) error {
	return invalidState("forum %s has no run %d", ref, run)
}

// ref names forum id of scope in a refusal (Ref), with the name in its
// current configuration; the ID alone when that cannot be read.
func (s *Service) ref(scope Scope, id string) string {
	store, err := s.open(scope, id)
	if err != nil {
		return id
	}
	return storeRef(store)
}

// storeRef is ref for an open store (the forum's or one of its runs').
func storeRef(store *Store) string {
	raw, _, err := store.ReadForumConfig()
	if err != nil {
		return store.ID()
	}
	return Ref(configName(raw), store.ID())
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

// refuseBusy refuses (errBusy) when the forum's latest run is busy. A
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
		return errBusy(storeRef(store), st.Status)
	}
	return nil
}

// configChanged reports whether the forum's current configuration (current,
// or forum.json read now when nil) differs from the one run used. The two
// are compared in canonical form (canonicalConfig), so formatting, member
// order and number spelling are not changes; the run's ConfigDigest stays
// the integrity check of its own copy.
func configChanged(run *Store, current []byte) (bool, error) {
	if current == nil {
		var err error
		if current, _, err = run.ReadForumConfig(); err != nil {
			return false, err
		}
	}
	used, err := run.ReadConfig()
	if err != nil {
		return false, corrupt("run %d %s: %v", run.RunNumber(), fileConfig, err)
	}
	// A document that does not decode is compared as different.
	a, errA := canonicalConfig(current)
	b, errB := canonicalConfig(used)
	same := errA == nil && errB == nil && bytes.Equal(a, b)
	return !same, nil
}

// canonicalConfig is a configuration in canonical form: decoded with
// numbers kept as written, each number normalised (an integer as an
// integer, any other as the shortest float form), and encoded again with
// object members in sorted order and no HTML escaping.
func canonicalConfig(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the configuration")
	}
	return marshalCompact(canonicalNumbers(v))
}

// canonicalNumbers replaces every json.Number in v with its normal form.
func canonicalNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = canonicalNumbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = canonicalNumbers(e)
		}
	case json.Number:
		if n, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
			return json.Number(strconv.FormatInt(n, 10))
		}
		if f, err := strconv.ParseFloat(x.String(), 64); err == nil {
			if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
				return json.Number(strconv.FormatInt(int64(f), 10))
			}
			return json.Number(strconv.FormatFloat(f, 'g', -1, 64))
		}
	}
	return v
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

// nextRun is the number of a forum's next run: one more than the highest
// of its started runs and of the runs its cleanup markers still name (an
// undone launch whose agents are still to delete), so a new run never
// shares a marker with an earlier one.
func nextRun(store *Store, started []int) (int, error) {
	last := 0
	if len(started) > 0 {
		last = started[len(started)-1]
	}
	for _, name := range []string{CleanupAgents, cleanupNotice} {
		marked, err := store.MarkedRuns(name)
		if err != nil {
			return 0, err
		}
		if len(marked) > 0 {
			last = max(last, marked[len(marked)-1])
		}
	}
	return last + 1, nil
}

// revertRun undoes a run whose launch did not finish: the temporary agents
// created so far are deleted (those that could not be stay in the run's
// agents marker, which the keep-alive loop and recovery retry; no later
// run takes that number, see nextRun), the notice marker cleared and the
// run's directory removed, so the forum is as it was before the launch.
// The caller holds the lock. It runs even if the launching call was
// cancelled.
func (s *Service) revertRun(ctx context.Context, store *Store) error {
	if err := s.deleteTempAgents(context.WithoutCancel(ctx), store); err != nil {
		s.host.Logger.Warnf("forum %s: undoing run %d: %v (retried every %s)", store.ID(), store.RunNumber(), err, s.keepAliveEvery)
		s.registerCleanup(Scope{AgentID: store.owner, BaseDirectory: store.base}, store)
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

// supersede ends a run that a later launch replaces (the caller has checked
// that the service is not closing): a paused run (or one
// left unfinished by a crash during such a launch) is cancelled and its
// terminal work done without a completion notice, since the launcher
// started the new run itself; a run whose records are damaged only has its
// temporary agents deleted and its notice marker cleared. The caller holds
// the lock.
func (s *Service) supersede(ctx context.Context, store *Store, damaged bool) error {
	id, n := store.ID(), store.RunNumber()
	s.takeLaunchChat(keyOf(store)) // superseded without a notice
	s.forgetNotice(keyOf(store))
	if damaged {
		s.forgetPaused(id)
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
		// Still paused: it stays registered for keep-alive.
		return err
	}
	// The cancel is committed; a crash from here leaves a cancelling run
	// that recovery finishes, still without a notice. Only now does the run
	// stop being a paused one to keep alive.
	s.forgetPaused(id)
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
