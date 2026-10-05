// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Seam (e): the lifecycle service (spec §9). One Service per process owns
// every running controller, the per-forum locks, temporary-agent cleanup
// and the completion notice. The tools in tools.go are thin wrappers over
// it.
//
// Host wiring (what ClawEh calls, in order):
//
//	svc := forum.New(forum.Host{Messenger: ..., Agents: ..., Notifier: ...,
//	        Logger: ..., Schemas: forum.JSONSchemaValidator{}},
//	        forum.WithHostLimits(...))                 // optional ceilings
//	err := svc.Recover(ctx, scopes)                    // once at startup, every agent's <workspace>/forums
//	defs := forum.Tools(svc, toolHost)                 // mount under "forum", gated by the forum permission
//	...
//	err = svc.Close(ctx)                               // at shutdown
//
// Every ID-only operation takes the caller's Scope and never looks outside
// Scope.BaseDirectory.

// keepAliveInterval is how often a paused forum's temporary agents are
// touched so they outlive the registry's idle TTL (§9, DESIGN.md §7.10).
const keepAliveInterval = time.Hour

// cleanupNotice is the marker (<base>/.cleanup/<uuid>.notice) of a
// completion notice not yet delivered. It is written at launch and cleared
// once the launcher has been notified, so a restart between the terminal
// commit and the notice still delivers it (§9 Completion).
const cleanupNotice = "notice"

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
	// Origin is where the completion notice goes. An empty AgentID is
	// filled with Scope.AgentID.
	Origin Origin
	// ConfigDir is the directory relative source paths resolve against:
	// the configuration file's directory for a file reference, the
	// launching agent's workspace for an inline configuration.
	ConfigDir string
	// ReadAllowed reports whether the launching agent may read an absolute
	// path (source files). nil allows nothing.
	ReadAllowed func(absPath string) error
}

// controller is what the service uses of a *Controller. Open returns the
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

// openFunc opens a locked store for execution (Open by default).
type openFunc func(ctx context.Context, s *Store, host Host) (controller, error)

// openController adapts Open to openFunc.
func openController(ctx context.Context, s *Store, host Host) (controller, error) {
	c, err := Open(ctx, s, host)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Service owns the forums of one process.
type Service struct {
	host       Host
	hostLimits Limits
	openCtrl   openFunc
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
	// keepAlive touches.
	paused map[string]*Store
	// controls serialises the control operations (pause, resume, cancel,
	// delete) of one forum ID.
	controls  map[string]*sync.Mutex
	keepAlive sync.Once
	wg        sync.WaitGroup
}

// run is one active controller with its store and lock. It stays in
// Service.runs until the run goroutine has finished the forum's terminal
// work or registered its pause and released the lock; done is closed after
// that.
type run struct {
	store  *Store
	ctrl   controller
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
	ctx, stop := context.WithCancel(context.Background())
	s := &Service{
		host:           host,
		openCtrl:       openController,
		keepAliveEvery: keepAliveInterval,
		ctx:            ctx,
		stop:           stop,
		runs:           map[string]*run{},
		paused:         map[string]*Store{},
		controls:       map[string]*sync.Mutex{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// stateError is ErrInvalidState with a message naming the forum and why
// the operation is refused.
type stateError struct{ msg string }

func (e *stateError) Error() string { return e.msg }
func (e *stateError) Unwrap() error { return ErrInvalidState }

// invalidState builds a *stateError from a message that starts with
// "forum <id>".
func invalidState(format string, args ...any) error {
	return &stateError{msg: fmt.Sprintf(format, args...)}
}

// errClosed is returned by operations that would start a run after Close.
var errClosed = errors.New("the forum service is shut down")

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
//  2. New UUID; CreateStore(opts.Scope.BaseDirectory, id) and Lock it at
//     once, so a concurrent Recover never mistakes the half-written root
//     for an abandoned launch; WriteConfig(raw).
//  3. Materialise every source (WriteSource): file sources from
//     Resolved.SourceContents, inline ones from the configuration.
//  4. Create the temporary participants used by enabled layers
//     (createParticipants), recording each in the CleanupAgents marker as
//     it is created.
//  5. WriteParticipants; set the notice marker; WriteSnapshot;
//     AppendCommit(CommitLaunched). The forum is now durably accepted.
//  6. Open and start the run goroutine; return id.
//
// Launch is all or nothing for its caller: any failure, including Open
// failing after step 5 (nothing has been dispatched yet), deletes the
// agents created so far and removes the directory, and the error is
// returned.
func (s *Service) Launch(ctx context.Context, raw []byte, opts LaunchOptions) (string, error) {
	if s.isClosed() {
		return "", errClosed
	}
	cfg, resolved, err := s.check(ctx, raw, opts)
	if err != nil {
		return "", err
	}
	if opts.Origin.AgentID == "" {
		opts.Origin.AgentID = opts.Scope.AgentID
	}
	store, err := CreateStore(opts.Scope.BaseDirectory, uuid.NewString())
	if err != nil {
		return "", err
	}
	if err = store.Lock(); err != nil {
		return "", errors.Join(err, store.Remove())
	}
	if err = s.allocate(ctx, store, raw, cfg, resolved, opts); err != nil {
		return "", errors.Join(err, s.discard(ctx, store))
	}
	ctrl, err := s.openCtrl(ctx, store, s.host)
	if err != nil {
		return "", errors.Join(fmt.Errorf("forum %s could not start: %w", store.ID(), err), s.discard(ctx, store))
	}
	if err = s.start(store, ctrl); err != nil { //nolint:contextcheck // the run outlives the launching call; its context is the service's
		return "", errors.Join(err, s.discard(ctx, store))
	}
	s.host.Logger.Infof("forum %s: launched by agent %s", store.ID(), opts.Scope.AgentID)
	return store.ID(), nil
}

// discard undoes a launch that did not start: it deletes the temporary
// agents created so far and removes the directory (which also releases
// the lock). It runs even if the launching call was cancelled.
func (s *Service) discard(ctx context.Context, store *Store) error {
	ctx = context.WithoutCancel(ctx)
	agentsErr := s.deleteTempAgents(ctx, store)
	if agentsErr != nil {
		s.host.Logger.Warnf("forum %s: discarding a failed launch: %v (the registry's idle TTL removes them)", store.ID(), agentsErr)
	}
	return store.Remove()
}

// allocate performs Launch steps 2 (after the lock) to 5.
func (s *Service) allocate(ctx context.Context, store *Store, raw []byte, cfg *Config, resolved *Resolved, opts LaunchOptions) error {
	if err := store.WriteConfig(raw); err != nil {
		return err
	}
	sources, err := materialiseSources(store, cfg, resolved)
	if err != nil {
		return err
	}
	parts, err := s.createParticipants(ctx, store, cfg, resolved, opts.Scope.AgentID)
	if err != nil {
		return err
	}
	if err = store.WriteParticipants(parts); err != nil {
		return err
	}
	if err = store.SetCleanup(cleanupNotice, []byte("pending\n")); err != nil {
		return err
	}
	seed, err := launchSeed(cfg)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	layers := make([]string, 0, len(cfg.Layers))
	for _, l := range cfg.EnabledLayers() {
		layers = append(layers, l.ID)
	}
	snap := &Snapshot{
		ForumID:          store.ID(),
		Name:             cfg.Name,
		LaunchedAt:       now,
		BaseDirectory:    opts.Scope.BaseDirectory,
		Deadline:         now.Add(time.Duration(cfg.Limits.MaxDurationSeconds) * time.Second),
		Origin:           opts.Origin,
		ConfigDigest:     digest(raw),
		Seed:             seed,
		Limits:           cfg.Limits,
		Layers:           layers,
		ResultLayers:     cfg.EffectiveResultLayers(),
		Models:           resolved.Models,
		ModeratorSchemas: resolved.ModeratorSchemas,
		Sources:          sources,
	}
	if err = store.WriteSnapshot(snap); err != nil {
		return err
	}
	_, err = store.AppendCommit(&Commit{Kind: CommitLaunched})
	return err
}

// materialiseSources copies every source into sources/: a file source
// from the bytes Preflight read (the file is never reopened), an inline one from the configuration
// (a JSON string's text for text and markdown, the raw JSON value for
// json).
func materialiseSources(store *Store, cfg *Config, resolved *Resolved) (map[string]SourceRecord, error) {
	out := make(map[string]SourceRecord, len(cfg.Sources))
	for _, id := range sortedKeys(cfg.Sources) {
		src := cfg.Sources[id]
		var content []byte
		switch {
		case src.File != "":
			data, ok := resolved.SourceContents[id]
			if !ok {
				return nil, fmt.Errorf("source %q: file %q was not read by preflight", id, src.File)
			}
			content = data
		case src.Decode == FormatJSON:
			content = []byte(src.Inline)
		default:
			var text string
			if err := json.Unmarshal(src.Inline, &text); err != nil {
				return nil, fmt.Errorf("source %q: inline content is not a JSON string: %w", id, err)
			}
			content = []byte(text)
		}
		rec, err := store.WriteSource(id, src.Decode, content)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", id, err)
		}
		out[id] = rec
	}
	return out, nil
}

// launchSeed is the configured seed, or a random nonnegative one.
func launchSeed(cfg *Config) (int64, error) {
	if cfg.Seed != nil {
		return *cfg.Seed, nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("generate seed: %w", err)
	}
	return int64(binary.LittleEndian.Uint64(b[:]) >> 1), nil
}

// createParticipants creates the temporary agents of every clone and
// fresh participant used by an enabled layer (as a participant or a
// moderator) and returns the records of every used participant, existing
// ones included; participants only named by disabled layers get no record.
// Each created agent is added to the CleanupAgents marker before the next
// is created, so whatever happens next its deletion is pending. On failure
// the caller discards the forum, which deletes the agents in the marker.
func (s *Service) createParticipants(ctx context.Context, store *Store, cfg *Config, resolved *Resolved, launcher string) (*Participants, error) {
	used := (&preflight{cfg: cfg}).usedParticipants()
	out := &Participants{Participants: make(map[string]ParticipantRecord, len(used))}
	var created []string
	for _, id := range used {
		part := cfg.Participants[id]
		rec := ParticipantRecord{ID: id, Form: part.Form(), Name: part.Name}
		if rec.Name == "" {
			rec.Name = id
		}
		var err error
		switch rec.Form {
		case FormExisting:
			rec.AgentID = part.Agent
		case FormClone:
			rec.Model = resolved.Models[id]
			rec.AgentID, err = s.host.Agents.CreateClone(ctx, CloneSpec{Source: part.Clone, Model: rec.Model, Owner: launcher})
			if err != nil {
				return nil, fmt.Errorf("participant %q: could not clone agent %q: %w", id, part.Clone, err)
			}
		case FormFresh:
			rec.Model = resolved.Models[id]
			rec.Mode = part.Mode
			if rec.Mode == "" {
				rec.Mode = FreshModeMemory
			}
			rec.AgentID, err = s.host.Agents.CreateFresh(ctx, FreshSpec{
				Model: rec.Model, SystemPrompt: part.SystemPrompt, Mode: rec.Mode, Owner: launcher,
			})
			if err != nil {
				return nil, fmt.Errorf("participant %q: could not create a temporary agent on model %q: %w", id, rec.Model, err)
			}
		}
		if rec.Form != FormExisting {
			rec.Created = true
			created = append(created, rec.AgentID)
			if err := setAgentsMarker(store, created); err != nil {
				return nil, err
			}
		}
		out.Participants[id] = rec
	}
	return out, nil
}

// Status returns one forum's summary.
func (s *Service) Status(_ context.Context, scope Scope, id string) (*Summary, error) {
	if r, ok := s.running(scope, id); ok {
		st := r.ctrl.State()
		return summaryOf(r.ctrl.Config(), r.ctrl.Snapshot(), &st), nil
	}
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

// List returns a summary of every forum in the scope, newest first. A
// forum that cannot be read is logged and left out.
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
	sort.SliceStable(out, func(i, j int) bool { return out[i].LaunchedAt.After(out[j].LaunchedAt) })
	return out, nil
}

// Pause asks a forum to pause and returns at once; the status becomes
// paused when in-flight turns finish (Controller.RequestPause). Repeating
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
			return r.ctrl.RequestPause()
		case StatusPausing, StatusPaused:
			return nil
		case StatusCancelling, StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
			return refusePause(id, st)
		}
	}
	store, st, err := s.takeOver(scope, id)
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
		if err := markLaunched(store, st.Status); err != nil {
			store.Unlock()
			return err
		}
		return s.openAndStart(ctx, store, controller.RequestPause)
	case StatusCancelling, StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
	}
	store.Unlock()
	return refusePause(id, st.Status)
}

func refusePause(id string, st Status) error {
	if st == StatusCancelling {
		return invalidState("forum %s is being cancelled and cannot be paused", id)
	}
	return invalidState("forum %s is %s and cannot be paused", id, st)
}

// Resume restarts a paused forum, or a queued, running or pausing one with
// no live controller in this process (interrupted). It commits
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
			return invalidState("forum %s is still pausing; resume it once it is paused", id)
		case StatusPaused, StatusCancelling, StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
			return refuseResume(id, st)
		}
	}
	store, st, err := s.takeOver(scope, id)
	if err != nil {
		return err
	}
	return s.resume(ctx, store, st)
}

func refuseResume(id string, st Status) error {
	if st == StatusCancelling {
		return invalidState("forum %s is being cancelled and cannot be resumed", id)
	}
	return invalidState("forum %s is %s and cannot be resumed", id, st)
}

// resume is Resume after the store is locked and its state loaded. It
// consumes the lock: it is held by the started run or released.
func (s *Service) resume(ctx context.Context, store *Store, st *State) error {
	var commit CommitKind
	switch st.Status {
	case StatusPaused, StatusPausing:
		commit = CommitResumed
	case StatusQueued:
		commit = CommitLaunched
	case StatusRunning:
	case StatusCancelling, StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
		store.Unlock()
		return refuseResume(store.ID(), st.Status)
	}
	if commit != "" {
		if _, err := store.AppendCommit(&Commit{Kind: commit}); err != nil {
			store.Unlock()
			return err
		}
	}
	s.forgetPaused(store.ID())
	return s.openAndStart(ctx, store, nil)
}

// Cancel stops a forum and finalises it as cancelled, keeping its partial
// work: a live one through Controller.RequestCancel; a paused or
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
		case StatusCompleted, StatusIncomplete, StatusFailed:
			return invalidState("forum %s is already %s", id, st)
		case StatusQueued, StatusRunning, StatusPausing, StatusPaused:
			return r.ctrl.RequestCancel()
		}
	}
	store, st, err := s.takeOver(scope, id)
	if err != nil {
		return err
	}
	switch st.Status {
	case StatusCancelled:
		store.Unlock()
		return nil
	case StatusCompleted, StatusIncomplete, StatusFailed:
		store.Unlock()
		return invalidState("forum %s is already %s", id, st.Status)
	case StatusCancelling:
		return s.openAndStart(ctx, store, nil) // the controller completes the cancel
	case StatusQueued, StatusRunning, StatusPausing, StatusPaused:
	}
	s.forgetPaused(id)
	return s.openAndStart(ctx, store, controller.RequestCancel)
}

// Results returns result.json for a terminal forum, or a partial manifest
// (resultOf over the current state, Complete false) for any other.
func (s *Service) Results(_ context.Context, scope Scope, id string) (*Result, error) {
	if r, ok := s.running(scope, id); ok {
		if st := r.ctrl.State(); !st.Status.Terminal() {
			return resultOf(r.ctrl.Config(), r.ctrl.Snapshot(), &st), nil
		}
	}
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

// Delete removes a paused or terminal forum: it takes the forum's lock
// without waiting (ErrLocked when another holder has it), rechecks the
// status, deletes the temporary agents (deleteTempAgents) and then the
// directory (Store.Remove). A forum whose directory is corrupt can be
// deleted too, since it can never run again. Any other status is
// ErrInvalidState; a forum whose agents could not all be deleted is kept
// and the error returned, so a retry finishes the job. Deleting a forum ID
// that no longer exists succeeds.
func (s *Service) Delete(ctx context.Context, scope Scope, id string) error {
	defer s.control(id)()
	r, err := s.live(ctx, scope, id)
	if err != nil {
		return err
	}
	if r != nil {
		return invalidState("forum %s is %s; pause or cancel it before deleting it", id, r.ctrl.State().Status)
	}
	store, err := s.open(scope, id)
	if errors.Is(err, ErrNotFound) && validForumID(id) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = store.Lock(); err != nil {
		return err
	}
	if _, _, st, err := load(store); err == nil {
		if st.Status != StatusPaused && !st.Status.Terminal() {
			store.Unlock()
			return invalidState("forum %s is %s; pause or cancel it before deleting it", id, st.Status)
		}
	} else if !errors.Is(err, ErrCorrupt) {
		store.Unlock()
		return err
	}
	if err := s.deleteTempAgents(ctx, store); err != nil {
		store.Unlock()
		return fmt.Errorf("forum %s was not deleted because its temporary agents could not all be deleted: %w", id, err)
	}
	s.forgetPaused(id)
	if err := store.Remove(); err != nil {
		return err
	}
	s.host.Logger.Infof("forum %s: deleted", id)
	return nil
}

// Recover is called once at startup with every agent's scope (§2.2: the
// binding is rebuilt by scanning workspaces; §2 "resume unfinished work
// after reboot"). Per scope it first removes roots staged for deletion
// (ListStaged, RemoveStaged), then applies recoverOne to every forum in
// ListForums. Errors are logged per forum and do not stop the scan; the
// returned error is the first one, for the caller's log line.
func (s *Service) Recover(ctx context.Context, scopes []Scope) error {
	var first error
	note := func(err error) {
		if first == nil {
			first = err
		}
	}
	for _, scope := range scopes {
		staged, err := ListStaged(scope.BaseDirectory)
		if err != nil {
			s.host.Logger.Errorf("forum recovery in %s: %v", scope.BaseDirectory, err)
			note(err)
		}
		for _, id := range staged {
			if rmErr := RemoveStaged(scope.BaseDirectory, id); rmErr != nil {
				s.host.Logger.Errorf("forum %s: finishing its removal: %v", id, rmErr)
				note(fmt.Errorf("forum %s: %w", id, rmErr))
			}
		}
		ids, err := ListForums(scope.BaseDirectory)
		if err != nil {
			s.host.Logger.Errorf("forum recovery in %s: %v", scope.BaseDirectory, err)
			note(err)
			continue
		}
		for _, id := range ids {
			if err := s.recoverOne(ctx, scope, id); err != nil {
				s.host.Logger.Errorf("forum %s: recover: %v", id, err)
				note(fmt.Errorf("forum %s: %w", id, err))
			}
		}
	}
	return first
}

// recoverOne applies Recover's rules to one forum (DESIGN.md §7.18):
//
//   - a launch that died before its snapshot was written is discarded
//     (its created agents deleted, its directory removed);
//   - a terminal forum gets its unfinished terminal work done
//     (completeTerminal: result.json, agent deletion, notice);
//   - a paused forum stays paused, its agents are touched once and it is
//     registered for keep-alive;
//   - a queued or running forum is resumed (queued gets CommitLaunched);
//   - a pausing or cancelling forum is resumed without a commit, so the
//     controller completes the pending pause or cancel.
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
	if _, err = store.ReadSnapshot(); errors.Is(err, ErrNotFound) {
		s.host.Logger.Warnf("forum %s: the launch did not finish; removing it", id)
		return s.discard(ctx, store)
	}
	cfg, snap, st, err := load(store)
	if err != nil {
		store.Unlock()
		return err
	}
	switch st.Status {
	case StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
		defer store.Unlock()
		s.completeTerminal(ctx, store, cfg, snap, st)
		return nil
	case StatusPaused:
		store.Unlock()
		s.touchForum(ctx, store)
		s.registerPaused(store)
		return nil
	case StatusQueued:
		if err := markLaunched(store, st.Status); err != nil {
			store.Unlock()
			return err
		}
	case StatusRunning, StatusPausing, StatusCancelling:
	}
	s.host.Logger.Infof("forum %s: resuming after restart (%s)", id, st.Status)
	return s.openAndStart(ctx, store, nil)
}

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

// open resolves a forum ID within the scope: OpenStore(scope.BaseDirectory,
// id), ErrNotFound when absent.
func (s *Service) open(scope Scope, id string) (*Store, error) {
	return OpenStore(scope.BaseDirectory, id)
}

// takeOver opens a forum that has no live controller in this process,
// takes its lock without waiting and loads its state. The caller owns the
// lock on success.
func (s *Service) takeOver(scope Scope, id string) (*Store, *State, error) {
	store, err := s.open(scope, id)
	if err != nil {
		return nil, nil, err
	}
	if err = store.Lock(); err != nil {
		return nil, nil, err
	}
	_, _, st, err := load(store)
	if err != nil {
		store.Unlock()
		return nil, nil, err
	}
	return store, st, nil
}

// markLaunched appends the CommitLaunched a launch did not get to write
// (status queued after a crash); any other status needs nothing.
func markLaunched(store *Store, st Status) error {
	if st != StatusQueued {
		return nil
	}
	_, err := store.AppendCommit(&Commit{Kind: CommitLaunched})
	return err
}

// openAndStart opens a locked store, applies before to the controller (a
// pause or cancel request), and starts the run. It consumes the lock: on
// failure the lock is released.
func (s *Service) openAndStart(ctx context.Context, store *Store, before func(controller) error) error {
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
	select {
	case <-r.done:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// control locks the control operations of one forum ID and returns the
// unlock function.
func (s *Service) control(id string) func() {
	s.mu.Lock()
	m, ok := s.controls[id]
	if !ok {
		m = &sync.Mutex{}
		s.controls[id] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// start runs ctrl in a goroutine whose context is the service's own,
// cancelled by Close, not the launching call's. The run is in s.runs
// until it has stopped and its follow-up is done: for a terminal status,
// completeTerminal; for a pause, registration for keep-alive. The lock is
// released and the run removed from s.runs in one step, so a caller that
// no longer sees the run can take the lock.
func (s *Service) start(store *Store, ctrl controller) error {
	id := store.ID()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errClosed
	}
	if _, ok := s.runs[id]; ok {
		s.mu.Unlock()
		return invalidState("forum %s is already running", id)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	r := &run{store: store, ctrl: ctrl, cancel: cancel, done: make(chan struct{})}
	s.runs[id] = r
	s.mu.Unlock()
	s.wg.Go(func() {
		defer close(r.done)
		defer cancel()
		status, err := ctrl.Run(ctx)
		if err != nil {
			s.host.Logger.Errorf("forum %s: run stopped: %v", id, err)
			status = ctrl.State().Status
		}
		if status.Terminal() {
			st := ctrl.State()
			s.completeTerminal(ctx, store, ctrl.Config(), ctrl.Snapshot(), &st)
		}
		s.mu.Lock()
		store.Unlock()
		delete(s.runs, id)
		if status == StatusPaused {
			s.paused[id] = store
		}
		s.mu.Unlock()
		if status == StatusPaused {
			s.host.Logger.Infof("forum %s: paused", id)
			s.ensureKeepAlive()
		}
	})
	return nil
}

// completeTerminal does the work that follows a terminal state, each step
// only if still pending, so a restart can repeat it (§9 Completion):
//
//  1. result.json is written if the controller did not get to it;
//  2. the temporary agents are deleted (deleteTempAgents);
//  3. the launcher is notified with result.json, only once it is
//     committed, and the notice marker is cleared.
//
// The store must be locked by the caller. Failures are logged.
func (s *Service) completeTerminal(ctx context.Context, store *Store, cfg *Config, snap *Snapshot, st *State) {
	id := store.ID()
	ctx = context.WithoutCancel(ctx)
	res, resErr := store.ReadResult()
	if errors.Is(resErr, ErrNotFound) {
		res = resultOf(cfg, snap, st)
		resErr = store.WriteResult(res)
	}
	if err := s.deleteTempAgents(ctx, store); err != nil {
		s.host.Logger.Warnf("forum %s: temporary agents: %v (retried at the next start)", id, err)
	}
	if resErr != nil {
		s.host.Logger.Errorf("forum %s: result: %v (the launcher is notified once it is written)", id, resErr)
		return
	}
	_, pending, err := store.Cleanup(cleanupNotice)
	if err != nil {
		s.host.Logger.Errorf("forum %s: notice marker: %v", id, err)
		return
	}
	if !pending {
		return
	}
	if err := s.host.Notifier.ForumFinished(ctx, snap.Origin, res); err != nil {
		s.host.Logger.Warnf("forum %s: notifying agent %s: %v", id, snap.Origin.AgentID, err)
	} else {
		s.host.Logger.Infof("forum %s: %s; agent %s notified", id, res.Status, snap.Origin.AgentID)
	}
	if err := store.ClearCleanup(cleanupNotice); err != nil {
		s.host.Logger.Warnf("forum %s: notice marker: %v", id, err)
	}
}

// agentsMarker reads the CleanupAgents marker: the temporary agents still
// to be deleted (nil when the marker is absent).
func agentsMarker(store *Store) ([]string, error) {
	data, ok, err := store.Cleanup(CleanupAgents)
	if err != nil || !ok {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, fmt.Errorf("%w: forum %s: cleanup marker %s: %w", ErrCorrupt, store.ID(), CleanupAgents, err)
	}
	return ids, nil
}

// setAgentsMarker writes the CleanupAgents marker, or clears it when ids
// is empty.
func setAgentsMarker(store *Store, ids []string) error {
	if len(ids) == 0 {
		return store.ClearCleanup(CleanupAgents)
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return store.SetCleanup(CleanupAgents, data)
}

// deleteTempAgents deletes every agent in the forum's CleanupAgents
// marker through Agents.Delete. The marker, written at launch as each
// agent is created, is the record of what is still to delete: once it is
// gone there is nothing to do, so repeating the call (or a restart) never
// deletes twice. Delete failing with ErrNotFound counts as done. Agents
// that could not be deleted stay in the marker for the next attempt (the
// registry's TTL is the backstop); the error names them.
func (s *Service) deleteTempAgents(ctx context.Context, store *Store) error {
	ids, err := agentsMarker(store)
	if err != nil || len(ids) == 0 {
		return err
	}
	var remaining []string
	var failures error
	for _, agentID := range ids {
		err := s.host.Agents.Delete(ctx, agentID)
		if err == nil || errors.Is(err, ErrNotFound) {
			continue
		}
		remaining = append(remaining, agentID)
		failures = errors.Join(failures, fmt.Errorf("agent %s: %w", agentID, err))
	}
	if err := setAgentsMarker(store, remaining); err != nil {
		return errors.Join(failures, err)
	}
	if failures != nil {
		return fmt.Errorf("deleting temporary agents of forum %s: %w", store.ID(), failures)
	}
	s.host.Logger.Debugf("forum %s: deleted temporary agents %v", store.ID(), ids)
	return nil
}

// registerPaused adds a paused forum to the keep-alive set and starts the
// keep-alive loop if needed.
func (s *Service) registerPaused(store *Store) {
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

// ensureKeepAlive starts the keep-alive goroutine once.
func (s *Service) ensureKeepAlive() {
	s.keepAlive.Do(func() {
		s.wg.Go(s.runKeepAlive)
	})
}

// runKeepAlive touches the temporary agents of every paused forum every
// keepAliveEvery until the service closes.
func (s *Service) runKeepAlive() {
	t := time.NewTicker(s.keepAliveEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.touchPaused(s.ctx)
		}
	}
}

// touchPaused calls Agents.Touch for every temporary agent of every
// paused forum, logging failures.
func (s *Service) touchPaused(ctx context.Context) {
	s.mu.Lock()
	stores := make([]*Store, 0, len(s.paused))
	for _, st := range s.paused {
		stores = append(stores, st)
	}
	s.mu.Unlock()
	for _, store := range stores {
		s.touchForum(ctx, store)
	}
}

// touchForum touches the temporary agents of one forum (those in its
// CleanupAgents marker), logging failures.
func (s *Service) touchForum(ctx context.Context, store *Store) {
	ids, err := agentsMarker(store)
	if err != nil {
		s.host.Logger.Warnf("forum %s: keep-alive: %v", store.ID(), err)
		return
	}
	for _, agentID := range ids {
		if err := s.host.Agents.Touch(ctx, agentID); err != nil {
			s.host.Logger.Warnf("forum %s: keep-alive of temporary agent %s: %v", store.ID(), agentID, err)
		}
	}
}

// summaryOf builds a Summary from the loaded records: every configured
// layer in configuration order, with progress for the enabled ones.
func summaryOf(cfg *Config, snap *Snapshot, st *State) *Summary {
	sum := &Summary{
		ForumID:    snap.ForumID,
		Name:       snap.Name,
		Status:     st.Status,
		Reason:     st.Reason,
		LaunchedAt: snap.LaunchedAt,
		UpdatedAt:  st.UpdatedAt,
		Deadline:   snap.Deadline,
		Calls:      st.Calls,
		MaxCalls:   snap.Limits.MaxCalls,
		Layers:     make([]LayerProgress, 0, len(cfg.Layers)),
	}
	for _, l := range cfg.Layers {
		p := LayerProgress{LayerID: l.ID, Enabled: slices.Contains(snap.Layers, l.ID), MaxRounds: l.MaxRounds}
		if ls := st.Layers[l.ID]; ls != nil {
			p.Started, p.Ended, p.EndReason = ls.Started, ls.Ended, ls.EndReason
			p.Round, p.Calls, p.Outputs = ls.Round, ls.Calls, len(ls.Outputs)
		}
		sum.Layers = append(sum.Layers, p)
	}
	return sum
}

// resultOf builds a Result (partial unless the status is terminal) from
// the loaded records: the result layers in snapshot order with their
// committed outputs. Omissions (DESIGN.md §8.9) name each result layer
// that did not start or did not end, and each participant turn of a
// result layer's rounds 1..Round (the rounds attempted) that has no
// committed output.
func resultOf(cfg *Config, snap *Snapshot, st *State) *Result {
	res := &Result{
		ForumID:    snap.ForumID,
		Name:       snap.Name,
		Status:     st.Status,
		Reason:     st.Reason,
		LaunchedAt: snap.LaunchedAt,
		Complete:   st.Status == StatusCompleted,
		Calls:      st.Calls,
		Layers:     make([]LayerResult, 0, len(snap.ResultLayers)),
		Transcript: fileTranscript,
	}
	if st.Status.Terminal() {
		res.EndedAt = st.UpdatedAt
	}
	for _, id := range snap.ResultLayers {
		ls := st.Layers[id]
		if ls == nil {
			ls = &LayerState{}
		}
		res.Layers = append(res.Layers, LayerResult{
			LayerID:   id,
			Ended:     ls.Ended,
			EndReason: ls.EndReason,
			Outputs:   append([]OutputRecord{}, ls.Outputs...),
		})
		switch {
		case !ls.Started:
			res.Omissions = append(res.Omissions, fmt.Sprintf("layer %s did not start", id))
			continue
		case !ls.Ended:
			res.Omissions = append(res.Omissions, fmt.Sprintf("layer %s did not end", id))
		}
		layer, ok := cfg.Layer(id)
		if !ok {
			continue
		}
		committed := make(map[string]bool, len(ls.Outputs))
		for _, o := range ls.Outputs {
			committed[o.Turn] = true
		}
		for round := 1; round <= ls.Round; round++ {
			for _, pid := range layer.Participants {
				if !committed[TurnID(round, pid)] {
					res.Omissions = append(res.Omissions, fmt.Sprintf("layer %s round %d: no output from %s", id, round, pid))
				}
			}
		}
	}
	return res
}
