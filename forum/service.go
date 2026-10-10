// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

// The lifecycle service. One Service per process owns
// every running controller, the per-forum locks, temporary-agent cleanup
// and the completion notice. The tools in tools.go are thin wrappers over
// it. This file holds the Service, its per-forum state and the helpers
// every operation shares; the operations are in service_*.go by concern.
//
// Host wiring (what ClawEh calls, in order):
//
//	svc := forum.New(forum.Host{Messenger: ..., Agents: ..., Notifier: ...,
//	        Logger: ...,
//	        OnStuck: ..., Cooldown: ...},              // optional
//	        forum.WithCeilings(...))                   // optional install-wide maximums
//	err := svc.Recover(ctx, scopes)                    // once at startup, every agent's <workspace>/forums
//	defs := forum.Tools(svc, toolHost)                 // mount under "forum", gated by the forum permission
//	...
//	err = svc.Close(ctx)                               // at shutdown, before the agent loop stops
//
// The host's side of the contract, beyond the interfaces' signatures:
// Messenger.Ask reports a host shutdown as ErrShuttingDown (never as a
// cancelled reply); ToolHost.Scope refuses calls made inside a forum turn
// with ErrForumTurn or ErrForumDepth; Notifier.ForumFinished hands the notice off; OnStuck
// does not block.
//
// Every ID-only operation takes the caller's Scope and never looks outside
// Scope.BaseDirectory.

// Scope is the caller's base scope: its agent ID and the absolute base
// directory its forums live in (<workspace>/forums). Every ID-only
// operation resolves the forum inside that directory and nowhere else.
type Scope struct {
	AgentID       string
	BaseDirectory string
}

// LaunchOptions is what a launch needs besides the configuration bytes.
type LaunchOptions struct {
	Scope Scope
	// Origin is where the completion notice goes. An empty AgentID is
	// filled with Scope.AgentID.
	Origin Origin
	// ResolveFile maps a source `file` reference to the absolute path the
	// launching agent's file tools would read (preflightEnv.ResolveFile).
	ResolveFile func(ref string) (absPath string, err error)
	// ReadAllowed reports whether the launching agent may read an absolute
	// path (source files). nil allows nothing.
	ReadAllowed func(absPath string) error
}

// controller is what the service uses of a *forumController. openForum returns the
// real one; tests substitute a fake through Service.openCtrl.
type controller interface {
	Run(ctx context.Context) (Status, error)
	RequestPause() error
	RequestCancel() error
	State() State
	Snapshot() *Snapshot
	Config() *Config
	Participants() *Participants
}

// openFunc opens a locked store for execution (openForum by default).
type openFunc func(ctx context.Context, s *forumStore, host Host) (controller, error)

// openController adapts openForum to openFunc.
func openController(ctx context.Context, s *forumStore, host Host) (controller, error) {
	c, err := openForum(ctx, s, host)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Service owns the forums of one process.
type Service struct {
	host Host
	// ceilings returns the install's current maximums (WithCeilings); nil
	// is none.
	ceilings func() Ceilings
	openCtrl openFunc
	// keepAliveEvery is keepAliveInterval; tests shorten it.
	keepAliveEvery time.Duration

	// ctx is the service's lifetime: every run and the keep-alive loop
	// derive from it, and Close cancels it.
	ctx  context.Context
	stop context.CancelFunc

	mu     sync.Mutex
	closed bool
	runs   map[string]*run
	// paused holds the stores of paused forums whose temporary agents
	// keepAlive touches (running forums' agents are touched through runs).
	paused map[string]*forumStore
	// cleanups holds the runs whose temporary agents could not all be
	// deleted at their terminal state; keepAlive retries them.
	cleanups map[runKey]Scope
	// controls serialises the control operations (pause, resume, cancel,
	// delete) of one forum ID; an entry lives while an operation holds or
	// waits for it.
	controls map[string]*controlLock
	// stuck lists the runs Host.OnStuck was called for in this process,
	// so it is called once; a run that later stops cleanly clears it.
	stuck map[runKey]bool
	// notifying lists the runs whose completion notice is being
	// delivered, so a notice is never sent twice concurrently.
	notifying map[runKey]bool
	// launchChats holds the chat each run was launched from, as the
	// launching tool call reported it. It is kept in memory only: the
	// origin recorded in snapshot.json lives in the launcher's workspace
	// and is not trusted, so a run whose launch this process did not see
	// (one resumed after a restart) has no entry.
	launchChats map[runKey]Chat
	// notices holds the runs whose completion notice (or the result.json it
	// waits for) failed; keepAlive retries them (noticeFailed).
	notices   map[runKey]*noticeRetry
	keepAlive sync.Once
	wg        sync.WaitGroup
}

// runKey names one run of one forum.
type runKey struct {
	id  string
	run int
}

// keyOf is the runKey of a run's store.
func keyOf(store *forumStore) runKey { return runKey{id: store.ID(), run: store.RunNumber()} }

// controlLock is one forum ID's control mutex with the number of
// operations holding or waiting for it.
type controlLock struct {
	mu   sync.Mutex
	refs int
}

// run is one active controller with its run's store and the forum's lock.
// At most one run of a forum is active. It stays in
// Service.runs until the run goroutine has finished the forum's terminal
// work or registered its pause and released the lock; done is closed after
// that.
type run struct {
	store  *forumStore
	ctrl   controller
	cancel context.CancelFunc
	done   chan struct{}
}

// Option configures a Service.
type Option func(*Service)

// WithCeilings sets the install's maximums on every configuration's limits
// (DESIGN.md §7.5). current is called at every validation and launch, so a
// change takes effect for the next one; runs already launched keep the
// limits they started with.
func WithCeilings(current func() Ceilings) Option {
	return func(s *Service) { s.ceilings = current }
}

// Ceilings returns the install's current maximums; the zero value (no
// ceiling) when none were set.
func (s *Service) Ceilings() Ceilings {
	if s.ceilings == nil {
		return Ceilings{}
	}
	return s.ceilings()
}

// New builds a service over the host dependencies. host.OnStuck and
// host.Cooldown may be nil; the other fields are required and New panics
// on a nil one, since that is a wiring error, not a runtime condition.
func New(host Host, opts ...Option) *Service {
	if host.Messenger == nil || host.Agents == nil || host.Notifier == nil || host.Logger == nil {
		panic("forum.New: Messenger, Agents, Notifier and Logger are required")
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &Service{
		host:           host,
		openCtrl:       openController,
		keepAliveEvery: keepAliveInterval,
		ctx:            ctx,
		stop:           stop,
		runs:           map[string]*run{},
		paused:         map[string]*forumStore{},
		cleanups:       map[runKey]Scope{},
		controls:       map[string]*controlLock{},
		stuck:          map[runKey]bool{},
		notifying:      map[runKey]bool{},
		launchChats:    map[runKey]Chat{},
		notices:        map[runKey]*noticeRetry{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// stateError is ErrInvalidState with a message naming the forum and why
// the operation is refused, and optionally a more specific cause.
type stateError struct {
	msg   string
	cause error
}

func (e *stateError) Error() string { return e.msg }

func (e *stateError) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrInvalidState}
	}
	return []error{ErrInvalidState, e.cause}
}

// invalidState builds a *stateError from a message that starts with
// "forum <id>".
func invalidState(format string, args ...any) error {
	return &stateError{msg: fmt.Sprintf(format, args...)}
}

// errClosed is returned by operations that would start a run after Close.
var errClosed = errors.New("the forum service is shut down")

// Close stops every running controller without changing its state on disk
// (an interrupted forum resumes at the next Recover), stops the
// keep-alive loop, waits for the goroutines to return or ctx to end, and
// releases the locks. Launch and Resume fail after Close.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stop()
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

func (s *Service) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// open resolves a forum ID within the scope: openStore(scope.BaseDirectory,
// id), ErrNotFound when absent. A forum whose forum-meta.json or latest
// run names another launcher is ErrCorrupt wrapping errForeign
// (checkForumOwner, checkOwner): the directory lives in the agent's
// workspace and is not trusted for whose it is.
func (s *Service) open(scope Scope, id string) (*forumStore, error) {
	store, err := openStore(scope.BaseDirectory, id)
	if err != nil {
		return nil, err
	}
	store.owner = scope.AgentID
	if err := checkForumOwner(store); err != nil {
		return nil, err
	}
	if n, runErr := latestRun(store); runErr == nil && n > 0 {
		if snap, snapErr := store.Run(n).ReadSnapshot(); snapErr == nil {
			if err := checkOwner(store, snap); err != nil {
				return nil, err
			}
		}
	}
	return store, nil
}

// running returns the live run for a forum ID in this process, provided
// it belongs to scope.
func (s *Service) running(scope Scope, id string) (*run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok || filepath.Clean(r.store.base) != filepath.Clean(scope.BaseDirectory) {
		return nil, false
	}
	return r, true
}

// live returns the live run of a forum, or nil when there is none. A run
// whose controller has already stopped (paused or terminal) is waited for,
// so the caller then sees the forum on disk with its lock released.
func (s *Service) live(ctx context.Context, scope Scope, id string) (*run, error) {
	r, ok := s.running(scope, id)
	if !ok {
		return nil, nil
	}
	if st := r.ctrl.State().Status; st != StatusPaused && !st.Terminal() {
		return r, nil
	}
	return nil, waitDone(ctx, r)
}

// waitDone waits until a run has been released (its done channel closed)
// or ctx ends.
func waitDone(ctx context.Context, r *run) error {
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// control locks the control operations of one forum ID and returns the
// unlock function. The entry is removed once no operation holds or waits
// for it, so the map never outgrows the operations in progress.
func (s *Service) control(id string) func() {
	s.mu.Lock()
	cl, ok := s.controls[id]
	if !ok {
		cl = &controlLock{}
		s.controls[id] = cl
	}
	cl.refs++
	s.mu.Unlock()
	cl.mu.Lock()
	return func() {
		cl.mu.Unlock()
		s.mu.Lock()
		if cl.refs--; cl.refs == 0 {
			delete(s.controls, id)
		}
		s.mu.Unlock()
	}
}

// forgetNotice drops a run from the notice retries.
func (s *Service) forgetNotice(key runKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.notices, key)
}

// logForum names a forum in a log line: "forum <ref>", the forum named as
// messages name it (storeRef).
func logForum(store *forumStore) string {
	return "forum " + storeRef(store)
}

// logRun names a run in a log line: "forum <ref> run <n>".
func logRun(store *forumStore) string {
	return fmt.Sprintf("%s run %d", logForum(store), store.RunNumber())
}

// registerPaused adds a paused forum to the keep-alive set and starts the
// keep-alive loop if needed.
func (s *Service) registerPaused(store *forumStore) {
	s.mu.Lock()
	s.paused[store.ID()] = store
	s.mu.Unlock()
	s.ensureKeepAlive()
}

// forgetPaused drops a forum from the keep-alive set.
func (s *Service) forgetPaused(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.paused, id)
}

// registerCleanup records a run whose temporary agents are still to
// delete, for the keep-alive loop to retry.
func (s *Service) registerCleanup(scope Scope, store *forumStore) {
	s.mu.Lock()
	s.cleanups[keyOf(store)] = scope
	s.mu.Unlock()
	s.ensureKeepAlive()
}

// forgetCleanup drops a run from the cleanup retries.
func (s *Service) forgetCleanup(key runKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cleanups, key)
}

// forgetRuns drops every run of a deleted forum from the cleanup and
// notice retries and forgets their launching chats.
func (s *Service) forgetRuns(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.cleanups {
		if key.id == id {
			delete(s.cleanups, key)
		}
	}
	for key := range s.notices {
		if key.id == id {
			delete(s.notices, key)
		}
	}
	for key := range s.launchChats {
		if key.id == id {
			delete(s.launchChats, key)
		}
	}
}

// setLaunchChat records the chat run key was launched from.
func (s *Service) setLaunchChat(key runKey, chat Chat) {
	if chat == (Chat{}) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.launchChats[key] = chat
}

// launchChat returns the chat run key was launched from, or the zero Chat
// when this process did not see the launch.
func (s *Service) launchChat(key runKey) Chat {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.launchChats[key]
}

// takeLaunchChat forgets the chat run key was launched from.
func (s *Service) takeLaunchChat(key runKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.launchChats, key)
}
