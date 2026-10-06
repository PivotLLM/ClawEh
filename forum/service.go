// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Seam (e): the lifecycle service (spec §9). One Service per process owns
// every running controller, the per-forum locks, temporary-agent cleanup
// and the completion notice. The tools in tools.go are thin wrappers over
// it.
//
// Host wiring (what ClawEh calls, in order):
//
//	svc := forum.New(forum.Host{Messenger: ..., Agents: ..., Notifier: ...,
//	        Logger: ..., Schemas: forum.JSONSchemaValidator{},
//	        OnStuck: ...},                             // optional: tell the launcher, raise an alert
//	        forum.WithHostLimits(...))                 // optional ceilings
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

// keepAliveInterval is how often the temporary agents of paused and
// running forums are touched so they outlive the registry's idle TTL, and
// how often pending temporary-agent deletions are retried (§9, DESIGN.md
// §7.10).
const keepAliveInterval = time.Hour

// cleanupNotice is the marker (<base>/.cleanup/<uuid>.<run>.notice) of a
// run's completion notice not yet delivered. It is written at launch and
// cleared once the launcher has been notified, so a restart between the
// terminal commit and the notice still delivers it (§9 Completion).
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
	// ResolveFile maps a source `file` reference to the absolute path the
	// launching agent's file tools would read (PreflightEnv.ResolveFile).
	ResolveFile func(ref string) (absPath string, err error)
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
	// keepAlive touches (running forums' agents are touched through runs).
	paused map[string]*Store
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
	keepAlive sync.Once
	wg        sync.WaitGroup
}

// runKey names one run of one forum.
type runKey struct {
	id  string
	run int
}

// keyOf is the runKey of a run's store.
func keyOf(store *Store) runKey { return runKey{id: store.ID(), run: store.RunNumber()} }

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
		cleanups:       map[runKey]Scope{},
		controls:       map[string]*controlLock{},
		stuck:          map[runKey]bool{},
		notifying:      map[runKey]bool{},
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
	if cfg == nil {
		return nil, nil, err
	}
	// Every problem is reported at once: Decode's and ValidateStatic's.
	decodeErr, _ := errors.AsType[*ValidationError](err)
	if staticErr := ValidateStatic(cfg); staticErr != nil || decodeErr != nil {
		var issues []Issue
		if decodeErr != nil {
			issues = append(issues, decodeErr.Issues...)
		}
		if ve, ok := errors.AsType[*ValidationError](staticErr); ok {
			issues = append(issues, ve.Issues...)
		} else if staticErr != nil {
			return nil, nil, staticErr
		}
		return nil, nil, &ValidationError{Issues: issues}
	}
	resolved, err := Preflight(ctx, cfg, PreflightEnv{
		Launcher:    opts.Scope.AgentID,
		Agents:      s.host.Agents,
		Schemas:     s.host.Schemas,
		HostLimits:  s.hostLimits,
		ResolveFile: opts.ResolveFile,
		ReadAllowed: opts.ReadAllowed,
	})
	if err != nil {
		return nil, nil, err
	}
	return cfg, resolved, nil
}

// Launch validates the forum's current configuration and starts a new run
// of it from the beginning, numbered after the latest (§9 forum_launch),
// and returns the run's number. Steps, in order:
//
//  1. Serialise with the forum's other control operations and Lock the
//     forum (ErrLocked when another process has it). A running forum is
//     refused (errRunning). Runs whose launch did not finish are undone
//     first (undoUnstarted).
//  2. check (Decode, ValidateStatic, Preflight) the configuration.
//  3. CreateRun; write the run's forum.json (the configuration, indented).
//  4. Materialise every source (WriteSource): file sources from
//     Resolved.SourceContents, inline ones from the configuration.
//  5. Create the temporary participants used by enabled layers
//     (createParticipants), recording each in the run's agents marker as
//     it is created.
//  6. WriteParticipants; set the run's notice marker; WriteSnapshot;
//     AppendCommit(CommitLaunched). The run is now durably accepted.
//  7. Open the run; end a paused previous run (supersede); start the run
//     goroutine.
//
// Launch is all or nothing for its caller: any failure, including Open
// failing after step 6 (nothing has been dispatched yet), deletes the
// agents created so far and removes the run (revertRun), leaving the forum
// as it was, and the error is returned.
func (s *Service) Launch(ctx context.Context, id string, opts LaunchOptions) (int, error) {
	if s.isClosed() {
		return 0, errClosed
	}
	defer s.control(id)()
	r, err := s.live(ctx, opts.Scope, id)
	if err != nil {
		return 0, err
	}
	if r != nil {
		return 0, errRunning(id)
	}
	store, err := s.open(opts.Scope, id)
	if err != nil {
		return 0, err
	}
	if err = store.Lock(); err != nil {
		return 0, err
	}
	n, err := s.launchLocked(ctx, store, opts)
	if err != nil {
		store.Unlock()
		return 0, err
	}
	s.host.Logger.Infof("forum %s: run %d launched by agent %s", id, n, opts.Scope.AgentID)
	return n, nil
}

// launchLocked is Launch once the forum is locked. On success the started
// run holds the lock; on failure the caller releases it.
func (s *Service) launchLocked(ctx context.Context, store *Store, opts LaunchOptions) (int, error) {
	started, err := s.undoUnstarted(ctx, store)
	if err != nil {
		return 0, err
	}
	var (
		prev        *Store
		prevDamaged bool
		prevPaused  bool
	)
	if len(started) > 0 {
		prev = store.Run(started[len(started)-1])
		_, _, st, loadErr := load(prev)
		switch {
		case errors.Is(loadErr, ErrCorrupt):
			prevDamaged = true
		case loadErr != nil:
			return 0, loadErr
		case busy(st.Status):
			return 0, errRunning(store.ID())
		default:
			prevPaused = st.Status == StatusPaused
		}
	}
	current, _, err := store.ReadForumConfig()
	if err != nil {
		return 0, err
	}
	cfg, resolved, err := s.check(ctx, current, opts)
	if err != nil {
		return 0, err
	}
	raw, err := formatConfig(current)
	if err != nil {
		return 0, err
	}
	if opts.Origin.AgentID == "" {
		opts.Origin.AgentID = opts.Scope.AgentID
	}
	n, err := nextRun(store, started)
	if err != nil {
		return 0, err
	}
	run, err := store.CreateRun(n)
	if err != nil {
		return 0, err
	}
	if err = s.allocate(ctx, run, raw, cfg, resolved, opts); err != nil {
		return 0, errors.Join(err, s.revertRun(ctx, run))
	}
	ctrl, err := s.openCtrl(ctx, run, s.host)
	if err != nil {
		return 0, errors.Join(fmt.Errorf("forum %s run %d could not start: %w", store.ID(), n, err), s.revertRun(ctx, run))
	}
	if prevPaused || prevDamaged {
		// A closing service could not start the new run; the paused one must
		// then stay as it is.
		if s.isClosed() {
			return 0, errors.Join(errClosed, s.revertRun(ctx, run))
		}
		if err = s.supersede(ctx, prev, prevDamaged); err != nil {
			return 0, errors.Join(err, s.revertRun(ctx, run))
		}
	}
	if err = s.start(run, ctrl); err != nil { //nolint:contextcheck // the run outlives the launching call; its context is the service's
		return 0, errors.Join(err, s.revertRun(ctx, run))
	}
	return n, nil
}

// allocate performs Launch steps 3 to 6 in the run's directory.
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
		Run:              store.RunNumber(),
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
	return store.AppendCommit(1, &Commit{Kind: CommitLaunched})
}

// materialiseSources copies every source into sources/: a file source
// from the bytes Preflight read (the file is never reopened), an inline one from the configuration
// (a JSON string's text for text and markdown, the JSON value indented on
// its own for json).
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
			var b bytes.Buffer
			if err := json.Indent(&b, src.Inline, "", "  "); err != nil {
				return nil, fmt.Errorf("source %q: inline content: %w", id, err)
			}
			content = b.Bytes()
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
	// Agents an earlier failed launch of this run could not delete stay
	// listed.
	created, err := agentsMarker(store)
	if err != nil {
		return nil, err
	}
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

// Status returns the summary of one run of a forum (its latest when run
// is 0), with the forum's run count and whether its configuration changed
// since the latest run; a forum with no run is StatusNew. It reads the
// records without verifying their digests (loadView); verification is
// Open's job.
func (s *Service) Status(_ context.Context, scope Scope, id string, run int) (*Summary, error) {
	store, err := s.open(scope, id)
	if err != nil {
		return nil, err
	}
	raw, mod, err := store.ReadForumConfig()
	if err != nil {
		return nil, err
	}
	runs, err := startedRuns(store)
	if err != nil {
		return nil, err
	}
	latest := 0
	if len(runs) > 0 {
		latest = runs[len(runs)-1]
	}
	if run == 0 {
		run = latest
	}
	if run == 0 {
		return &Summary{ForumID: id, Name: forumLabel(configName(raw), id), Status: StatusNew, UpdatedAt: mod, Layers: []LayerProgress{}}, nil
	}
	if !slices.Contains(runs, run) {
		return nil, errNoRun(id, run)
	}
	var sum *Summary
	if r, ok := s.running(scope, id); ok && r.store.RunNumber() == run {
		st := r.ctrl.State()
		sum = summaryOf(r.ctrl.Config(), r.ctrl.Snapshot(), &st)
	} else {
		cfg, snap, st, viewErr := loadView(store.Run(run))
		if viewErr != nil {
			return nil, viewErr
		}
		sum = summaryOf(cfg, snap, st)
	}
	sum.Runs = len(runs)
	if sum.ConfigChanged, err = configChanged(store.Run(latest), raw); err != nil {
		return nil, err
	}
	return sum, nil
}

// List returns the summary of every forum in the scope (its latest run),
// newest first (a new forum by when its configuration last changed). A
// forum that cannot be read is logged and left out.
func (s *Service) List(ctx context.Context, scope Scope) ([]Summary, error) {
	ids, err := ListForums(scope.BaseDirectory)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(ids))
	for _, id := range ids {
		sum, err := s.Status(ctx, scope, id, 0)
		if err != nil {
			s.host.Logger.Warnf("forum %s: status: %v", id, err)
			continue
		}
		out = append(out, *sum)
	}
	sort.SliceStable(out, func(i, j int) bool { return sortTime(&out[i]).After(sortTime(&out[j])) })
	return out, nil
}

// sortTime is when a forum's latest run was launched, or a new forum's
// configuration last changed.
func sortTime(sum *Summary) time.Time {
	if sum.Status == StatusNew {
		return sum.UpdatedAt
	}
	return sum.LaunchedAt
}

// Pause asks a forum's latest run to pause and returns at once; the status becomes
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
			return refusePause(id, st)
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
	return refusePause(id, st.Status)
}

func refusePause(id string, st Status) error {
	if st == StatusCancelling {
		return invalidState("forum %s is being cancelled and cannot be paused", id)
	}
	return invalidState("forum %s is %s and cannot be paused", id, st)
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
			return invalidState("forum %s is still pausing; resume it once it is paused", id)
		case StatusNew, StatusPaused, StatusCancelling, StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
			return refuseResume(id, st)
		}
	}
	store, snap, st, err := s.takeOver(scope, id)
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
			return invalidState("forum %s: the config changed; launch to start a new run", snap.Label())
		}
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
	case StatusNew, StatusCancelling, StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
		store.Unlock()
		return refuseResume(store.ID(), st.Status)
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
		case StatusNew, StatusCompleted, StatusIncomplete, StatusFailed:
			return invalidState("forum %s is already %s", id, st)
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
		return invalidState("forum %s is already %s", id, st.Status)
	case StatusCancelling:
		return s.openAndStart(ctx, store, nil) // the controller completes the cancel
	case StatusQueued, StatusRunning, StatusPausing, StatusPaused:
	}
	s.forgetPaused(id)
	return s.openAndStart(ctx, store, controller.RequestCancel)
}

// Results returns the result of one run of a forum (its latest when run is
// 0): result.json for a terminal run, or a partial manifest (buildResult
// over the current state, Complete false) for any other, built exactly as
// the controller builds result.json, so a round that was not published
// stays hidden. A forum with no run is refused.
func (s *Service) Results(_ context.Context, scope Scope, id string, run int) (*Result, error) {
	if r, ok := s.running(scope, id); ok && (run == 0 || run == r.store.RunNumber()) {
		if st := r.ctrl.State(); !st.Status.Terminal() {
			return buildResult(r.ctrl.Config(), r.ctrl.Snapshot(), &st), nil
		}
	}
	store, err := s.open(scope, id)
	if err != nil {
		return nil, err
	}
	runs, err := startedRuns(store)
	if err != nil {
		return nil, err
	}
	switch {
	case len(runs) == 0:
		return nil, errNotLaunched(id)
	case run == 0:
		run = runs[len(runs)-1]
	case !slices.Contains(runs, run):
		return nil, errNoRun(id, run)
	}
	rs := store.Run(run)
	// The run's own records are checked like the latest run's (open):
	// its snapshot must name this run and the scope's agent.
	snap, err := rs.ReadSnapshot()
	if err != nil {
		return nil, corrupt("run %d %s: %v", run, fileSnapshot, err)
	}
	if err = checkOwner(rs, snap); err != nil {
		return nil, err
	}
	if snap.ForumID != id || snap.Run != run {
		return nil, corrupt("run %d %s names forum %s run %d", run, fileSnapshot, snap.ForumID, snap.Run)
	}
	res, err := rs.ReadResult()
	if err == nil {
		if res.ForumID != id || res.Run != run {
			return nil, corrupt("run %d %s names forum %s run %d", run, fileResult, res.ForumID, res.Run)
		}
		return res, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	cfg, snap, st, err := loadView(rs)
	if err != nil {
		return nil, err
	}
	return buildResult(cfg, snap, st), nil
}

// Delete removes a forum and every run of it, unless its latest run is
// running: it takes the forum's lock without waiting (ErrLocked when
// another holder has it), rechecks the latest run's status, deletes the
// temporary agents of every run (deleteTempAgents) and then the directory
// (Store.Remove). A forum whose records are damaged can be deleted too.
// A running forum is refused (errRunning); a forum whose agents could not
// all be deleted is kept and the error returned, so a retry finishes the
// job. Deleting a forum ID that no longer exists succeeds.
func (s *Service) Delete(ctx context.Context, scope Scope, id string) error {
	defer s.control(id)()
	r, err := s.live(ctx, scope, id)
	if err != nil {
		return err
	}
	if r != nil {
		return errRunning(id)
	}
	store, err := s.open(scope, id)
	switch {
	case errors.Is(err, ErrNotFound) && validForumID(id):
		return nil
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
	marked, err := store.MarkedRuns(CleanupAgents)
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
	s.forgetCleanups(id)
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
// ListForums. Errors are logged per forum and do not stop the scan; a
// forum that cannot be reopened (other than one locked by another
// process) is also reported through Host.OnStuck. The returned error is
// the first one, for the caller's log line.
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
		s.removeIncomplete(scope)
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
				if !errors.Is(err, ErrLocked) && !errors.Is(err, errClosed) {
					s.reportStuck(scope, id, err)
				}
			}
		}
	}
	return first
}

// removeIncomplete removes the directories of forums whose creation died
// before forum.json was written (ListIncomplete). One locked by a forum
// being created right now is left alone.
func (s *Service) removeIncomplete(scope Scope) {
	ids, err := ListIncomplete(scope.BaseDirectory)
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
	s.host.Logger.Infof("forum %s: resuming run %d after restart (%s)", id, run.RunNumber(), st.Status)
	return s.openAndStart(ctx, run, nil)
}

// deleteLeftAgents deletes the temporary agents left in the agents marker
// of a run that no longer exists (a launch undone whose agents could not
// all be deleted). Failures are logged; the marker keeps them.
func (s *Service) deleteLeftAgents(ctx context.Context, store *Store, started []int) {
	marked, err := store.MarkedRuns(CleanupAgents)
	if err != nil {
		s.host.Logger.Warnf("forum %s: %v", store.ID(), err)
		return
	}
	for _, n := range marked {
		if slices.Contains(started, n) {
			continue
		}
		if err := s.deleteTempAgents(ctx, store.Run(n)); err != nil {
			s.host.Logger.Warnf("forum %s: run %d that did not start: %v", store.ID(), n, err)
		}
	}
}

// finishEarlier finishes the pending work of a run that is not the
// forum's latest: a terminal run with markers gets its terminal work done
// (completeTerminal); a run a crash left unfinished while a later run was
// being launched, markers or not, is superseded (cancelled without a
// notice), as that launch would have done. Failures are logged. The caller
// holds the lock.
func (s *Service) finishEarlier(ctx context.Context, run *Store) {
	_, agents, agentsErr := run.Cleanup(CleanupAgents)
	_, notice, noticeErr := run.Cleanup(cleanupNotice)
	if err := errors.Join(agentsErr, noticeErr); err != nil {
		s.host.Logger.Warnf("forum %s: run %d: %v", run.ID(), run.RunNumber(), err)
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
		s.host.Logger.Warnf("forum %s: finishing run %d: %v", run.ID(), run.RunNumber(), err)
	}
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
// id), ErrNotFound when absent. A forum whose forum-meta.json or latest
// run names another launcher is ErrCorrupt wrapping errForeign
// (checkForumOwner, checkOwner): the directory lives in the agent's
// workspace and is not trusted for whose it is.
func (s *Service) open(scope Scope, id string) (*Store, error) {
	store, err := OpenStore(scope.BaseDirectory, id)
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

// takeOver opens a forum that has no live controller in this process,
// takes its lock without waiting and loads its latest run's snapshot and
// state; a forum with no run is refused. The caller owns the lock on
// success; the returned store is the latest run's.
func (s *Service) takeOver(scope Scope, id string) (*Store, *Snapshot, *State, error) {
	store, err := s.open(scope, id)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = store.Lock(); err != nil {
		return nil, nil, nil, err
	}
	n, err := latestRun(store)
	if err == nil && n == 0 {
		err = errNotLaunched(id)
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
func markLaunched(store *Store, st *State) error {
	if st.Status != StatusQueued {
		return nil
	}
	return store.AppendCommit(st.Seq+1, &Commit{Kind: CommitLaunched})
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

// loadView reads what a read-only caller (status, list, results) needs:
// forum.json, snapshot.json and the current State (LoadState), without
// Verify's digest checks of every source and output.
func loadView(store *Store) (*Config, *Snapshot, *State, error) {
	snap, err := store.ReadSnapshot()
	if err != nil {
		return nil, nil, nil, corrupt("%s: %v", fileSnapshot, err)
	}
	if ownerErr := checkOwner(store, snap); ownerErr != nil {
		return nil, nil, nil, ownerErr
	}
	raw, err := store.ReadConfig()
	if err != nil {
		return nil, nil, nil, corrupt("%s: %v", fileConfig, err)
	}
	cfg, err := Decode(raw)
	if err != nil {
		return nil, nil, nil, corrupt("%s: %v", fileConfig, err)
	}
	st, err := LoadState(store, cfg, snap)
	if err != nil {
		return nil, nil, nil, err
	}
	return cfg, snap, st, nil
}

// load reads a forum's configuration, snapshot and current state after
// verifying the directory (Verify).
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

// start runs ctrl in a goroutine whose context is the service's own,
// cancelled by Close, not the launching call's. The run is in s.runs
// until it has stopped and its follow-up is done: for a terminal status,
// completeTerminal; for a pause, registration for keep-alive; for an error,
// the log line and, unless the host is shutting down, Host.OnStuck. The
// lock is released and the run removed from s.runs in one step, so a
// caller that no longer sees the run can take the lock.
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
	s.ensureKeepAlive()
	s.wg.Go(func() {
		defer close(r.done)
		defer cancel()
		status, err := drive(ctx, ctrl)
		if err != nil {
			status = ctrl.State().Status
			s.runFailed(ctrl.Snapshot(), err)
		} else {
			s.mu.Lock()
			delete(s.stuck, keyOf(store))
			s.mu.Unlock()
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
			s.host.Logger.Infof("forum %s: run %d paused", id, store.RunNumber())
		}
	})
	return nil
}

// drive runs ctrl until it settles. A pause or cancel request accepted
// just before Run returned (or while it was failing) leaves the forum
// pausing or cancelling with nothing to finish the transition, so while
// the state is pausing or cancelling Run is called again to settle it.
// The controller refuses requests once Run has returned (errRunEnded), so
// re-reading the state after Run returns sees every request it accepted.
// Two errors in a row stop the loop: the failure is persistent.
func drive(ctx context.Context, ctrl controller) (Status, error) {
	status, err := ctrl.Run(ctx)
	for {
		st := ctrl.State().Status
		if (st != StatusPausing && st != StatusCancelling) || st == status {
			return status, err
		}
		prev := err
		status, err = ctrl.Run(ctx)
		if err != nil && prev != nil {
			return status, err
		}
	}
}

// runFailed logs a run that stopped on an error and leaves it as it is on
// disk (it resumes with forum_resume or at the next start). A host
// shutdown is expected and logged at Info; anything else is logged at
// Error naming the forum and the run and reported once through
// Host.OnStuck.
func (s *Service) runFailed(snap *Snapshot, err error) {
	id := snap.ForumID
	if errors.Is(err, ErrShuttingDown) || (s.ctx.Err() != nil && errors.Is(err, context.Canceled)) {
		s.host.Logger.Infof("forum %s: run %d stopped by the shutdown; it resumes at the next start", id, snap.Run)
		return
	}
	s.host.Logger.Errorf("forum %s: run %d stopped and is stuck until it is resumed: %v", id, snap.Run, err)
	s.stuckOnce(runKey{id: id, run: snap.Run}, snap.Origin, err)
}

// reportStuck reports a forum Recover could not reopen through
// Host.OnStuck, with its latest run and origin when they are readable.
func (s *Service) reportStuck(scope Scope, id string, err error) {
	key := runKey{id: id}
	origin := Origin{AgentID: scope.AgentID}
	if store, openErr := s.open(scope, id); openErr == nil {
		if n, runErr := latestRun(store); runErr == nil && n > 0 {
			key.run = n
			if snap, snapErr := store.Run(n).ReadSnapshot(); snapErr == nil {
				origin = snap.Origin
			}
		}
	}
	s.stuckOnce(key, origin, err)
}

// stuckOnce calls Host.OnStuck for a run unless it was already called in
// this process.
func (s *Service) stuckOnce(key runKey, origin Origin, err error) {
	if s.host.OnStuck == nil {
		return
	}
	s.mu.Lock()
	seen := s.stuck[key]
	s.stuck[key] = true
	s.mu.Unlock()
	if !seen {
		s.host.OnStuck(key.id, key.run, origin, err)
	}
}

// completeTerminal does the work that follows a terminal state, each step
// only if still pending, so a restart can repeat it (§9 Completion):
//
//  1. result.json is written if the controller did not get to it;
//  2. the temporary agents are deleted (deleteTempAgents); a failure is
//     retried by the keep-alive loop until it succeeds;
//  3. the launcher is notified with result.json, only once it is
//     committed, and the notice marker is cleared (notify, on a goroutine
//     of its own so a blocking Notifier never holds the forum).
//
// The store must be locked by the caller. Failures are logged.
func (s *Service) completeTerminal(ctx context.Context, store *Store, cfg *Config, snap *Snapshot, st *State) {
	id := fmt.Sprintf("%s run %d", store.ID(), store.RunNumber())
	ctx = context.WithoutCancel(ctx)
	res, resErr := store.ReadResult()
	if errors.Is(resErr, ErrNotFound) {
		res = buildResult(cfg, snap, st)
		resErr = store.WriteResult(res)
	}
	if err := s.deleteTempAgents(ctx, store); err != nil {
		s.host.Logger.Warnf("forum %s: %v (retried every %s)", id, err, s.keepAliveEvery)
		s.registerCleanup(Scope{AgentID: snap.Origin.AgentID, BaseDirectory: store.base}, store)
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
	if pending {
		s.notify(store, snap.Origin, res)
	}
}

// notify delivers the completion notice on a goroutine of its own and
// clears the notice marker afterwards. A notice that fails because the
// service is closing keeps its marker, so the next start delivers it; any
// other failure is logged and not retried. A notice already in flight for
// the forum is not sent again.
func (s *Service) notify(store *Store, origin Origin, res *Result) {
	key := keyOf(store)
	id := fmt.Sprintf("%s run %d", key.id, key.run)
	s.mu.Lock()
	if s.notifying[key] {
		s.mu.Unlock()
		return
	}
	s.notifying[key] = true
	s.mu.Unlock()
	s.wg.Go(func() {
		defer func() {
			s.mu.Lock()
			delete(s.notifying, key)
			s.mu.Unlock()
		}()
		if err := s.host.Notifier.ForumFinished(s.ctx, origin, res); err != nil {
			s.host.Logger.Warnf("forum %s: notifying agent %s: %v", id, origin.AgentID, err)
			if s.ctx.Err() != nil {
				return
			}
		} else {
			s.host.Logger.Infof("forum %s: %s; agent %s notified", id, res.Status, origin.AgentID)
		}
		if err := store.ClearCleanup(cleanupNotice); err != nil {
			s.host.Logger.Warnf("forum %s: notice marker: %v", id, err)
		}
	})
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

// agentsLeftError is deleteTempAgents' failure: the temporary agents of a
// forum that could not be deleted (they stay in the marker for the next
// attempt).
type agentsLeftError struct {
	forumID string
	agents  []string
	err     error
}

func (e *agentsLeftError) Error() string {
	return fmt.Sprintf("forum %s: temporary agents %v could not be deleted: %v", e.forumID, e.agents, e.err)
}

func (e *agentsLeftError) Unwrap() error { return e.err }

// deleteTempAgents deletes every agent in the forum's CleanupAgents
// marker through Agents.Delete. The marker, written at launch as each
// agent is created, is the record of what is still to delete: once it is
// gone there is nothing to do, so repeating the call (or a restart) never
// deletes twice. Delete failing with ErrNotFound counts as done. Agents
// that could not be deleted stay in the marker for the next attempt (the
// keep-alive loop retries a terminal forum's, the registry's TTL is the
// backstop); the error is an *agentsLeftError naming them.
func (s *Service) deleteTempAgents(ctx context.Context, store *Store) error {
	ids, err := agentsMarker(store)
	if err != nil || len(ids) == 0 {
		return err
	}
	var remaining []string
	var failures error
	for _, agentID := range ids {
		err := s.host.Agents.Delete(ctx, store.owner, agentID)
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
		return &agentsLeftError{forumID: store.ID(), agents: remaining, err: failures}
	}
	s.host.Logger.Debugf("forum %s run %d: deleted temporary agents %v", store.ID(), store.RunNumber(), ids)
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

// registerCleanup records a run whose temporary agents are still to
// delete, for the keep-alive loop to retry.
func (s *Service) registerCleanup(scope Scope, store *Store) {
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

// forgetCleanups drops every run of a forum from the cleanup retries.
func (s *Service) forgetCleanups(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.cleanups {
		if key.id == id {
			delete(s.cleanups, key)
		}
	}
}

// ensureKeepAlive starts the keep-alive goroutine once.
func (s *Service) ensureKeepAlive() {
	s.keepAlive.Do(func() {
		s.wg.Go(s.runKeepAlive)
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
// forum, then retries the pending temporary-agent deletions.
func (s *Service) keepAliveTick(ctx context.Context) {
	s.mu.Lock()
	stores := make([]*Store, 0, len(s.paused)+len(s.runs))
	for _, st := range s.paused {
		stores = append(stores, st)
	}
	for _, r := range s.runs {
		stores = append(stores, r.store)
	}
	s.mu.Unlock()
	for _, store := range stores {
		s.touchForum(ctx, store)
	}
	s.retryCleanups(ctx)
}

// retryCleanups retries the temporary-agent deletion of every run in
// s.cleanups. A run whose forum is gone, or whose deletion succeeds, leaves
// the set; one whose forum is running or locked by another process is
// tried again at the next tick.
func (s *Service) retryCleanups(ctx context.Context) {
	s.mu.Lock()
	pending := maps.Clone(s.cleanups)
	s.mu.Unlock()
	keys := slices.SortedFunc(maps.Keys(pending), func(a, b runKey) int {
		if c := strings.Compare(a.id, b.id); c != 0 {
			return c
		}
		return a.run - b.run
	})
	for _, key := range keys {
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
		s.host.Logger.Warnf("forum %s: temporary agents: %v", key.id, err)
		return false
	}
	if err := store.Lock(); err != nil {
		return false
	}
	defer store.Unlock()
	if err := s.deleteTempAgents(ctx, store.Run(key.run)); err != nil {
		s.host.Logger.Warnf("forum %s run %d: %v (retried every %s)", key.id, key.run, err, s.keepAliveEvery)
		return false
	}
	s.host.Logger.Infof("forum %s run %d: its remaining temporary agents are deleted", key.id, key.run)
	return true
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
		if err := s.host.Agents.Touch(ctx, store.owner, agentID); err != nil {
			s.host.Logger.Warnf("forum %s: keep-alive of temporary agent %s: %v", store.ID(), agentID, err)
		}
	}
}

// summaryOf builds a Summary from the loaded records: every configured
// layer in configuration order, with progress for the enabled ones.
func summaryOf(cfg *Config, snap *Snapshot, st *State) *Summary {
	sum := &Summary{
		ForumID:    snap.ForumID,
		Name:       snap.Label(),
		Run:        snap.Run,
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
