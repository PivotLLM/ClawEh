// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Seam (e): the lifecycle service (spec §9). One Service per process owns
// every running controller, the per-forum locks, temporary-agent cleanup
// and the completion notice. The tools in tools.go are thin wrappers over
// it.

// keepAliveInterval is how often a paused forum's temporary agents are
// touched so they outlive the registry's idle TTL (§9). Proposed value;
// see DESIGN.md open questions.
const keepAliveInterval = time.Hour

// Scope is the caller's base scope: its agent ID and the absolute base
// directory its forums live in (<workspace>/forums, §2.2). Every ID-only
// operation resolves the forum inside that directory and nowhere else.
type Scope struct {
	AgentID       string
	BaseDirectory string
}

// LaunchOptions is what a launch needs besides the configuration bytes.
type LaunchOptions struct {
	Scope Scope
	// Origin is where the completion notice goes.
	Origin Origin
	// ConfigDir is the directory relative source paths resolve against:
	// the configuration file's directory for a file reference, the
	// launching agent's workspace for an inline configuration.
	ConfigDir string
	// ReadAllowed reports whether the launching agent may read an absolute
	// path (source files). nil allows nothing.
	ReadAllowed func(absPath string) error
}

// Service owns the forums of one process.
type Service struct {
	host       Host
	hostLimits Limits

	mu   sync.Mutex
	runs map[string]*run
	// paused lists the stores of paused forums whose temporary agents
	// keepAlive touches.
	paused    map[string]*Store
	keepAlive sync.Once
	wg        sync.WaitGroup
}

// run is one active controller with its store and lock.
type run struct {
	store  *Store
	ctrl   *Controller
	cancel context.CancelFunc
	done   chan struct{}
}

// Option configures a Service.
type Option func(*Service)

// WithHostLimits sets ceilings on every configuration's limits (§3 "host
// ceilings also apply"); a zero field is no ceiling.
func WithHostLimits(l Limits) Option {
	return func(s *Service) { s.hostLimits = l }
}

// New builds a service over the host dependencies. host.Schemas may be
// nil; the other fields are required and New panics on a nil one, since
// that is a wiring error, not a runtime condition.
func New(host Host, opts ...Option) *Service {
	if host.Messenger == nil || host.Agents == nil || host.Notifier == nil || host.Logger == nil {
		panic("forum.New: Messenger, Agents, Notifier and Logger are required")
	}
	s := &Service{host: host, runs: map[string]*run{}, paused: map[string]*Store{}}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Models lists the models agentID may give to fresh participants (§2.4):
// Agents.Models(agentID) as is.
func (s *Service) Models(ctx context.Context, agentID string) ([]ModelInfo, error) {
	return s.host.Agents.Models(ctx, agentID)
}

// Validate runs Decode, ValidateStatic and Preflight on raw without
// creating anything (§9 forum_validate). It returns nil, a
// *ValidationError listing every finding, or ErrSchemasUnavailable.
func (s *Service) Validate(ctx context.Context, raw []byte, opts LaunchOptions) error {
	_, _, err := s.check(ctx, raw, opts)
	return err
}

// check is the shared validation path of Validate and Launch: Decode,
// ValidateStatic, Preflight with the service's host limits.
func (s *Service) check(ctx context.Context, raw []byte, opts LaunchOptions) (*Config, *Resolved, error) {
	cfg, err := Decode(raw)
	if err != nil {
		return nil, nil, err
	}
	if err = ValidateStatic(cfg); err != nil {
		return nil, nil, err
	}
	resolved, err := Preflight(ctx, cfg, PreflightEnv{
		Launcher:    opts.Scope.AgentID,
		Agents:      s.host.Agents,
		Schemas:     s.host.Schemas,
		HostLimits:  s.hostLimits,
		ConfigDir:   opts.ConfigDir,
		ReadAllowed: opts.ReadAllowed,
	})
	if err != nil {
		return nil, nil, err
	}
	return cfg, resolved, nil
}

// Launch validates raw, allocates the forum and starts it (§9
// forum_launch). Steps, in order:
//
//  1. check (Decode, ValidateStatic, Preflight).
//  2. New UUID; CreateStore(opts.Scope.BaseDirectory, id); WriteConfig(raw).
//  3. Read each file source (Resolved.SourceFiles) and inline source;
//     WriteSource each (materialise).
//  4. Create temporary participants used by enabled layers
//     (createParticipants): CreateClone for clones (with the model
//     override), CreateFresh for fresh ones. If any creation fails, delete
//     those created so far, remove the directory and return the error.
//  5. WriteParticipants; WriteSnapshot (seed from the configuration or
//     crypto/rand, deadline = now + max_duration_seconds, Resolved.Models
//     and Directed, the enabled layer order, EffectiveResultLayers);
//     AppendCommit(CommitLaunched). The forum is now durably accepted.
//  6. Lock the store, Open, and start the run goroutine; return id.
//
// A failure before step 5 leaves nothing behind; after it, the forum
// exists and a later Recover resumes it.
func (s *Service) Launch(ctx context.Context, raw []byte, opts LaunchOptions) (string, error) {
	cfg, resolved, err := s.check(ctx, raw, opts)
	if err != nil {
		return "", err
	}
	store, snap, err := s.allocate(ctx, raw, cfg, resolved, opts)
	if err != nil {
		return "", err
	}
	if err = store.Lock(); err != nil {
		return "", err
	}
	ctrl, err := Open(ctx, store, s.host)
	if err != nil {
		store.Unlock()
		return "", err
	}
	s.start(store, ctrl) //nolint:contextcheck // the run outlives the launching call; its context is the service's
	return snap.ForumID, nil
}

// allocate performs Launch steps 2 to 5 and returns the store and the
// snapshot of the durably accepted forum.
func (s *Service) allocate(ctx context.Context, raw []byte, cfg *Config, resolved *Resolved, opts LaunchOptions) (*Store, *Snapshot, error) {
	id := uuid.NewString()
	store, err := CreateStore(opts.Scope.BaseDirectory, id)
	if err != nil {
		return nil, nil, err
	}
	if err = store.WriteConfig(raw); err != nil {
		return nil, nil, errors.Join(err, store.Remove())
	}
	parts, err := s.createParticipants(ctx, cfg, resolved, opts.Scope.AgentID)
	if err != nil {
		return nil, nil, errors.Join(err, store.Remove())
	}
	if err = store.WriteParticipants(parts); err != nil {
		return nil, nil, errors.Join(err, s.deleteTempAgents(ctx, store, parts), store.Remove())
	}
	return nil, nil, errNotImplemented
}

// createParticipants creates the temporary agents of every clone and
// fresh participant used by an enabled layer and returns the records for
// all participants, including existing ones. On failure it deletes the
// agents created so far and returns the error.
func (s *Service) createParticipants(ctx context.Context, cfg *Config, resolved *Resolved, launcher string) (*Participants, error) {
	return nil, errNotImplemented
}

// Status returns one forum's summary (summaryOf over LoadState).
func (s *Service) Status(ctx context.Context, scope Scope, id string) (*Summary, error) {
	store, err := s.open(scope, id)
	if err != nil {
		return nil, err
	}
	cfg, snap, st, err := load(store)
	if err != nil {
		return nil, err
	}
	return summaryOf(cfg, snap, st), nil
}

// List returns a summary of every forum in the scope, newest first.
func (s *Service) List(ctx context.Context, scope Scope) ([]Summary, error) {
	ids, err := ListForums(scope.BaseDirectory)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(ids))
	for _, id := range ids {
		sum, err := s.Status(ctx, scope, id)
		if err != nil {
			s.host.Logger.Warnf("forum %s: status: %v", id, err)
			continue
		}
		out = append(out, *sum)
	}
	return out, errNotImplemented
}

// Pause asks a running forum to pause (Controller.RequestPause) and
// returns at once; the status becomes paused when in-flight turns finish.
// ErrInvalidState unless running in this process. A paused forum's
// temporary agents are kept alive by keepAlive.
func (s *Service) Pause(ctx context.Context, scope Scope, id string) error {
	r, ok := s.running(id)
	if !ok {
		return ErrInvalidState
	}
	return r.ctrl.RequestPause()
}

// Resume restarts a paused forum (forum_resume), or a queued/running one
// with no live controller in this process (Recover's automatic resume
// failed earlier). It locks the store, commits CommitResumed when the
// status was paused, Opens and starts the run. ErrInvalidState for a
// terminal forum or one already running here; ErrLocked when another
// process holds it.
func (s *Service) Resume(ctx context.Context, scope Scope, id string) error {
	if _, ok := s.running(id); ok {
		return ErrInvalidState
	}
	store, err := s.open(scope, id)
	if err != nil {
		return err
	}
	return s.resume(ctx, store)
}

// resume is Resume after the store is resolved; Recover uses it too.
func (s *Service) resume(ctx context.Context, store *Store) error {
	return errNotImplemented
}

// Cancel stops a forum: a running one through Controller.RequestCancel; a
// paused or interrupted one by opening it, committing the cancel request
// and running it to its cancelled end. ErrInvalidState for a terminal
// forum. Cancelled is terminal: result.json is written, temporary agents
// deleted, the launcher notified.
func (s *Service) Cancel(ctx context.Context, scope Scope, id string) error {
	if r, ok := s.running(id); ok {
		return r.ctrl.RequestCancel()
	}
	store, err := s.open(scope, id)
	if err != nil {
		return err
	}
	_ = store
	return errNotImplemented
}

// Results returns result.json for a terminal forum, or a partial manifest
// (resultOf over LoadState, Complete false) for any other.
func (s *Service) Results(ctx context.Context, scope Scope, id string) (*Result, error) {
	store, err := s.open(scope, id)
	if err != nil {
		return nil, err
	}
	res, err := store.ReadResult()
	if err == nil {
		return res, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	cfg, snap, st, err := load(store)
	if err != nil {
		return nil, err
	}
	return resultOf(cfg, snap, st), nil
}

// Delete removes a paused or terminal forum: deletes its temporary agents
// (deleteTempAgents), then Store.Remove. ErrInvalidState for any other
// status; ErrLocked when another process holds it.
func (s *Service) Delete(ctx context.Context, scope Scope, id string) error {
	if _, ok := s.running(id); ok {
		return ErrInvalidState
	}
	store, err := s.open(scope, id)
	if err != nil {
		return err
	}
	_, _, st, err := load(store)
	if err != nil {
		return err
	}
	if st.Status != StatusPaused && !st.Status.Terminal() {
		return ErrInvalidState
	}
	parts, err := store.ReadParticipants()
	if err != nil {
		return err
	}
	if err := s.deleteTempAgents(ctx, store, parts); err != nil {
		return err
	}
	s.forgetPaused(id)
	return store.Remove()
}

// Recover is called once at startup with every agent's scope (§2.2: the
// binding is rebuilt by scanning workspaces; §2 "resume unfinished work
// after reboot"). Per scope it first removes roots staged for deletion
// (ListStaged, RemoveStaged), then for each forum in ListForums
// (recoverOne): a terminal forum gets deleteTempAgents if its
// CleanupAgents marker is pending; a paused forum stays paused and is
// registered for keepAlive; a queued or running forum is resumed
// automatically; a pausing or cancelling forum is resumed so the
// controller completes the pending transition (paused, or cancelled).
// Errors are logged per forum and do not stop the scan; the returned
// error is the first one, for the caller's log line.
func (s *Service) Recover(ctx context.Context, scopes []Scope) error {
	s.ensureKeepAlive(ctx)
	var first error
	for _, scope := range scopes {
		ids, err := ListForums(scope.BaseDirectory)
		if err != nil {
			first = errors.Join(first, err)
			continue
		}
		for _, id := range ids {
			if err := s.recoverOne(ctx, scope, id); err != nil {
				s.host.Logger.Errorf("forum %s: recover: %v", id, err)
				if first == nil {
					first = fmt.Errorf("forum %s: %w", id, err)
				}
			}
		}
	}
	return first
}

// recoverOne applies Recover's rules to one forum: open, load, and by
// status finish cleanup (terminal), register for keepAlive (paused) or
// resume (anything else).
func (s *Service) recoverOne(ctx context.Context, scope Scope, id string) error {
	return errNotImplemented
}

// Close stops every running controller without changing its state on disk
// (an interrupted forum resumes at the next Recover), waits for the run
// goroutines to return or ctx to end, and releases the locks.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	for _, r := range s.runs {
		r.cancel()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// open resolves a forum ID within the scope: OpenStore(scope.BaseDirectory,
// id), ErrNotFound when absent.
func (s *Service) open(scope Scope, id string) (*Store, error) {
	return OpenStore(scope.BaseDirectory, id)
}

// load reads a forum's configuration, snapshot and current state.
func load(store *Store) (*Config, *Snapshot, *State, error) {
	cfg, snap, err := Verify(store)
	if err != nil {
		return nil, nil, nil, err
	}
	st, err := LoadState(store, cfg, snap)
	if err != nil {
		return nil, nil, nil, err
	}
	return cfg, snap, st, nil
}

// running returns the live run for a forum ID in this process.
func (s *Service) running(id string) (*run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	return r, ok
}

// start runs ctrl in a goroutine: Controller.Run, then finish for a
// terminal status, or registration for keepAlive and unlock for a pause.
// The run is registered in s.runs for the duration. The run's context is
// the service's own, cancelled by Close, not the launching call's.
func (s *Service) start(store *Store, ctrl *Controller) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &run{store: store, ctrl: ctrl, cancel: cancel, done: make(chan struct{})}
	id := ctrl.Snapshot().ForumID
	s.mu.Lock()
	s.runs[id] = r
	s.mu.Unlock()
	s.wg.Go(func() {
		defer close(r.done)
		defer func() {
			s.mu.Lock()
			delete(s.runs, id)
			s.mu.Unlock()
		}()
		status, err := ctrl.Run(ctx)
		if err != nil {
			s.host.Logger.Errorf("forum %s: run stopped: %v", id, err)
			store.Unlock()
			return
		}
		if status.Terminal() {
			s.finish(ctx, store, ctrl)
			return
		}
		s.mu.Lock()
		s.paused[id] = store
		s.mu.Unlock()
		store.Unlock()
	})
}

// finish completes a terminal forum: deleteTempAgents (with the
// CleanupAgents marker set first and cleared after), then
// Notifier.ForumFinished with result.json, then unlock. result.json is
// already committed by the controller (§9 Completion: committed before
// the launcher is notified).
func (s *Service) finish(ctx context.Context, store *Store, ctrl *Controller) {
	defer store.Unlock()
	if err := s.deleteTempAgents(ctx, store, ctrl.Participants()); err != nil {
		s.host.Logger.Warnf("forum %s: temporary agents: %v", ctrl.Snapshot().ForumID, err)
	}
	res, err := store.ReadResult()
	if err != nil {
		s.host.Logger.Errorf("forum %s: result: %v", ctrl.Snapshot().ForumID, err)
		return
	}
	if err := s.host.Notifier.ForumFinished(ctx, ctrl.Snapshot().Origin, res); err != nil {
		s.host.Logger.Warnf("forum %s: notify: %v", ctrl.Snapshot().ForumID, err)
	}
}

// deleteTempAgents deletes every participant with Created true through
// Agents.Delete, logging and continuing on failure; agents that could not
// be deleted stay in the CleanupAgents marker for the next Recover, and
// the registry's TTL is the backstop. The error is the first failure.
func (s *Service) deleteTempAgents(ctx context.Context, store *Store, parts *Participants) error {
	return errNotImplemented
}

// forgetPaused drops a forum from the keep-alive set.
func (s *Service) forgetPaused(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.paused, id)
}

// ensureKeepAlive starts the keep-alive goroutine once.
func (s *Service) ensureKeepAlive(ctx context.Context) {
	s.keepAlive.Do(func() {
		s.wg.Go(func() { s.runKeepAlive(ctx) })
	})
}

// runKeepAlive touches the temporary agents of every paused forum every
// keepAliveInterval until ctx ends.
func (s *Service) runKeepAlive(ctx context.Context) {
	t := time.NewTicker(keepAliveInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.touchPaused(ctx)
		}
	}
}

// touchPaused calls Agents.Touch for every created participant of every
// paused forum, logging failures.
func (s *Service) touchPaused(ctx context.Context) {
}

// summaryOf builds a Summary from the loaded records.
func summaryOf(cfg *Config, snap *Snapshot, st *State) *Summary {
	return nil
}

// resultOf builds a Result (partial unless the status is terminal) from
// the loaded records: the result layers in snapshot order with their
// committed outputs.
func resultOf(cfg *Config, snap *Snapshot, st *State) *Result {
	return nil
}
