// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Test helpers of seam (e), prefixed svc so they cannot collide with the
// helpers of the other seams in the same package.

// svcConfigJSON has an existing participant (alice), a clone with a model
// override (bob), a fresh moderator (editor), a file, an inline text and an
// inline JSON source, and a disabled layer whose only participant (spare)
// must not be created.
const svcConfigJSON = `{
  "version": 1, "name": "svc-test",
  "brief": {"purpose": "Test the forum service.", "task": "Answer briefly."},
  "sources": {
    "note": {"decode": "text", "inline": "A short note."},
    "data": {"decode": "json", "inline": {"k": 1}},
    "doc":  {"decode": "markdown", "file": "doc.md"}
  },
  "participants": {
    "alice":  {"agent": "alice"},
    "bob":    {"clone": "bob", "model": "large", "name": "Bob"},
    "editor": {"model": "default", "system_prompt": "Be brief.", "mode": "context"},
    "spare":  {"model": "default"}
  },
  "limits": {"max_calls": 20, "max_duration_seconds": 600,
    "call_timeout_seconds": 60, "max_attempts_per_turn": 2, "max_parallel_calls": 2},
  "layers": [
    {"id": "talk", "participants": ["alice", "bob"], "instructions": "Discuss.",
     "inputs": [{"from": "source:note"}, {"from": "source:doc"}],
     "delivery": "after_round", "max_rounds": 2, "output": {"format": "text"},
     "moderator": {"participant": "editor", "after_round": 1, "every_rounds": 1}},
    {"id": "off", "enabled": false, "participants": ["spare"], "instructions": "Unused.",
     "delivery": "after_round", "max_rounds": 1, "output": {"format": "text"}}
  ]
}`

// svcSimpleJSON has no file source and no moderator: alice and a fresh bob
// for one round.
const svcSimpleJSON = `{
  "version": 1, "name": "svc-simple",
  "brief": {"purpose": "Test the forum service.", "task": "Answer briefly."},
  "participants": {"alice": {"agent": "alice"}, "bob": {"model": "default"}},
  "limits": {"max_calls": 10, "max_duration_seconds": 600,
    "call_timeout_seconds": 60, "max_attempts_per_turn": 2, "max_parallel_calls": 2},
  "layers": [
    {"id": "talk", "participants": ["alice", "bob"], "instructions": "Say hello.",
     "delivery": "after_round", "max_rounds": 1, "output": {"format": "text"}}
  ]
}`

// svcAgents is a fake Agents registry. Alice and Bob exist and may be
// targeted by the launcher; every agent has the models "default" and
// "large". Temporary agents get UUIDs.
type svcAgents struct {
	mu        sync.Mutex
	allowed   map[string]bool
	exists    map[string]bool
	models    map[string][]ModelInfo
	clones    []CloneSpec
	freshes   []FreshSpec
	created   []string
	deleted   []string
	touched   map[string]int
	launchers []string         // the launcher passed to every Delete and Touch
	createErr map[string]error // "clone:<source>" or "fresh:<model>"
	deleteErr map[string]error // by agent ID
	touchErr  error
}

func svcNewAgents() *svcAgents {
	two := []ModelInfo{{Name: "default", Provider: "p", Protocol: "openai"}, {Name: "large", Provider: "p", Protocol: "openai"}}
	return &svcAgents{
		allowed:   map[string]bool{"alice": true, "bob": true},
		exists:    map[string]bool{"alice": true, "bob": true, "launcher": true},
		models:    map[string][]ModelInfo{"launcher": two, "alice": two, "bob": two},
		touched:   map[string]int{},
		createErr: map[string]error{},
		deleteErr: map[string]error{},
	}
}

func (a *svcAgents) Exists(_ context.Context, id string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exists[id], nil
}

func (a *svcAgents) MayTarget(_ context.Context, _, target string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.allowed[target], nil
}

func (a *svcAgents) Models(_ context.Context, id string) ([]ModelInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.createErr["models"]; err != nil {
		return nil, err
	}
	return a.models[id], nil
}

func (a *svcAgents) newAgent() string {
	id := uuid.NewString()
	a.exists[id] = true
	a.created = append(a.created, id)
	return id
}

func (a *svcAgents) CreateClone(_ context.Context, spec CloneSpec) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.createErr["clone:"+spec.Source]; err != nil {
		return "", err
	}
	a.clones = append(a.clones, spec)
	return a.newAgent(), nil
}

func (a *svcAgents) CreateFresh(_ context.Context, spec FreshSpec) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.createErr["fresh:"+spec.Model]; err != nil {
		return "", err
	}
	a.freshes = append(a.freshes, spec)
	return a.newAgent(), nil
}

func (a *svcAgents) Delete(_ context.Context, launcher, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.launchers = append(a.launchers, launcher)
	if err := a.deleteErr[id]; err != nil {
		return err
	}
	a.deleted = append(a.deleted, id)
	delete(a.exists, id)
	return nil
}

func (a *svcAgents) Touch(_ context.Context, launcher, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.launchers = append(a.launchers, launcher)
	a.touched[id]++
	return a.touchErr
}

func (a *svcAgents) createdIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.created)
}

func (a *svcAgents) deletedIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.deleted)
}

func (a *svcAgents) touches(id string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.touched[id]
}

func (a *svcAgents) setDeleteErr(id string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		delete(a.deleteErr, id)
		return
	}
	a.deleteErr[id] = err
}

// svcNotifier records completion notices and, at each one, checks the
// order §9 requires: result.json is on disk and the forum's temporary
// agents are already deleted (no agents marker left).
type svcNotifier struct {
	base string
	err  error
	// block, when set, holds every notice until it is closed or ctx ends.
	block chan struct{}

	mu         sync.Mutex
	notices    []*Result
	origins    []Origin
	chats      []Chat
	violations []string
}

func (n *svcNotifier) ForumFinished(ctx context.Context, origin Origin, chat Chat, res *Result) error {
	if n.block != nil {
		select {
		case <-n.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notices = append(n.notices, res)
	n.origins = append(n.origins, origin)
	n.chats = append(n.chats, chat)
	if _, err := os.Stat(filepath.Join(n.base, res.ForumID, dirRuns, strconv.Itoa(res.Run), fileResult)); err != nil {
		n.violations = append(n.violations, "notice before result.json: "+err.Error())
	}
	if _, err := os.Stat(filepath.Join(n.base, dirCleanup, fmt.Sprintf("%s.%d.%s", res.ForumID, res.Run, CleanupAgents))); err == nil {
		n.violations = append(n.violations, "notice before the temporary agents were deleted")
	}
	return n.err
}

func (n *svcNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.notices)
}

func (n *svcNotifier) last() (*Result, Origin) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.notices) == 0 {
		return nil, Origin{}
	}
	return n.notices[len(n.notices)-1], n.origins[len(n.origins)-1]
}

// lastChat returns the launching chat of the latest notice.
func (n *svcNotifier) lastChat() Chat {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.chats) == 0 {
		return Chat{}
	}
	return n.chats[len(n.chats)-1]
}

func (n *svcNotifier) problems() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.violations)
}

// svcLogger collects log lines.
type svcLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *svcLogger) addf(level, format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, level+" "+fmt.Sprintf(format, v...))
}

func (l *svcLogger) Debugf(format string, v ...any) { l.addf("DEBUG", format, v...) }
func (l *svcLogger) Infof(format string, v ...any)  { l.addf("INFO", format, v...) }
func (l *svcLogger) Warnf(format string, v ...any)  { l.addf("WARN", format, v...) }
func (l *svcLogger) Errorf(format string, v ...any) { l.addf("ERROR", format, v...) }

func (l *svcLogger) has(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// svcMessenger fails every ask; the fake controller never asks.
type svcMessenger struct{}

func (svcMessenger) Ask(context.Context, string, string, time.Duration) (Reply, error) {
	return Reply{}, errors.New("svcMessenger: no asks expected")
}

// svcCtrl is a fake controller over a real store. It commits through the
// store and derives its state with LoadState, so what it leaves on disk is
// what the real controller would. Run waits until the test finishes it
// (finish), a pause or cancel is requested, or the service closes; a
// cancel request dominates a pause request.
type svcCtrl struct {
	store *Store
	cfg   *Config
	snap  *Snapshot
	parts *Participants

	mu sync.Mutex
	st *State

	pause, cancel atomic.Bool
	wake          chan struct{}
	finish        chan Status
	// fail makes a running Run return the error sent on it.
	fail        chan error
	started     chan struct{}
	startedOnce sync.Once
	runs        atomic.Int32
	// exited mirrors the real controller: set when Run returns, cleared
	// when it is called; requests are refused while it is set.
	exited atomic.Bool
	// beforeReturn, when set, runs as Run is about to return (a request
	// landing in that window).
	beforeReturn func(c *svcCtrl)
	// onRequest, when set, runs at the start of RequestPause and
	// RequestCancel.
	onRequest func(c *svcCtrl)
	// noResult makes end commit without writing result.json (a crash
	// between the two).
	noResult bool
}

func (c *svcCtrl) Run(ctx context.Context) (Status, error) {
	c.exited.Store(false)
	c.runs.Add(1)
	c.startedOnce.Do(func() { close(c.started) })
	st, err := c.run(ctx)
	if c.beforeReturn != nil {
		c.beforeReturn(c)
	}
	c.exited.Store(true)
	return st, err
}

func (c *svcCtrl) run(ctx context.Context) (Status, error) {
	switch st := c.State().Status; st {
	case StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
		return st, nil
	case StatusPausing:
		return c.finishPause()
	case StatusCancelling:
		return StatusCancelled, c.end(StatusCancelled, EndCancelled)
	case StatusNew, StatusQueued, StatusRunning, StatusPaused:
	}
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err := <-c.fail:
			return "", err
		case st := <-c.finish:
			reason := EndCompleted
			if st == StatusFailed {
				reason = EndHostError
			}
			return st, c.end(st, reason)
		case <-c.wake:
			if c.cancel.Load() {
				return StatusCancelled, c.end(StatusCancelled, EndCancelled)
			}
			if c.pause.Load() {
				return c.finishPause()
			}
		}
	}
}

func (c *svcCtrl) finishPause() (Status, error) {
	return StatusPaused, c.commit(&Commit{Kind: CommitPaused})
}

func (c *svcCtrl) end(st Status, reason EndReason) error {
	if err := c.commit(&Commit{Kind: CommitEnded, Status: st, Reason: reason}); err != nil {
		return err
	}
	if c.noResult {
		return nil
	}
	cur := c.State()
	return c.store.WriteResult(buildResult(c.cfg, c.snap, &cur))
}

func (c *svcCtrl) commit(commit *Commit) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := nextAppend(c.store, commit); err != nil {
		return err
	}
	st, err := ReplayState(c.store, c.cfg, c.snap) // as the real controller, the lock holder writes state.json
	if err != nil {
		return err
	}
	c.st = st
	return nil
}

func (c *svcCtrl) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *svcCtrl) RequestPause() error {
	if c.onRequest != nil {
		c.onRequest(c)
	}
	if c.exited.Load() {
		return runEnded(c.snap.ForumID, "paused")
	}
	if c.State().Status != StatusRunning {
		return ErrInvalidState
	}
	if err := c.commit(&Commit{Kind: CommitPauseRequested}); err != nil {
		return err
	}
	c.pause.Store(true)
	c.poke()
	return nil
}

func (c *svcCtrl) RequestCancel() error {
	if c.onRequest != nil {
		c.onRequest(c)
	}
	if c.exited.Load() {
		return runEnded(c.snap.ForumID, "cancelled")
	}
	if c.State().Status.Terminal() {
		return ErrInvalidState
	}
	if err := c.commit(&Commit{Kind: CommitCancelRequested}); err != nil {
		return err
	}
	c.cancel.Store(true)
	c.poke()
	return nil
}

func (c *svcCtrl) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.st
}

func (c *svcCtrl) Snapshot() *Snapshot         { return c.snap }
func (c *svcCtrl) Config() *Config             { return c.cfg }
func (c *svcCtrl) Participants() *Participants { return c.parts }

// svcCtrls opens fake controllers and keeps the latest one per forum.
type svcCtrls struct {
	mu      sync.Mutex
	byID    map[string]*svcCtrl
	opens   int
	openErr error
	// openErrRun fails opening the controller of one run number.
	openErrRun map[int]error
	noResult   bool
}

func (r *svcCtrls) open(_ context.Context, s *Store, _ Host) (controller, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opens++
	if r.openErr != nil {
		return nil, r.openErr
	}
	if err := r.openErrRun[s.RunNumber()]; err != nil {
		return nil, err
	}
	cfg, snap, st, err := load(s)
	if err != nil {
		return nil, err
	}
	parts, err := s.ReadParticipants()
	if err != nil {
		return nil, err
	}
	c := &svcCtrl{
		store: s, cfg: cfg, snap: snap, parts: parts, st: st,
		wake: make(chan struct{}, 1), finish: make(chan Status, 1), fail: make(chan error, 1), started: make(chan struct{}),
		noResult: r.noResult,
	}
	// The latest run's controller is the forum's; a superseded earlier run
	// opened after it does not replace it.
	if old, ok := r.byID[s.ID()]; !ok || old.store.RunNumber() <= s.RunNumber() {
		r.byID[s.ID()] = c
	}
	return c, nil
}

func (r *svcCtrls) openCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opens
}

// get waits for the controller of a forum to be opened and running.
func (r *svcCtrls) get(t *testing.T, id string) *svcCtrl {
	t.Helper()
	var c *svcCtrl
	svcEventually(t, "controller of "+id+" opened", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		c = r.byID[id]
		return c != nil
	})
	select {
	case <-c.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("controller of %s never ran", id)
	}
	return c
}

// svcEnv is one service under test with its fakes, a workspace and the
// launcher's scope (<workspace>/forums).
type svcEnv struct {
	t         *testing.T
	svc       *Service
	agents    *svcAgents
	notifier  *svcNotifier
	logger    *svcLogger
	ctrls     *svcCtrls
	workspace string
	scope     Scope
	stuck     *svcStuck
}

// ref is how messages name forum id: Ref with the name in its current
// configuration, or the ID once the forum is gone.
func (e *svcEnv) ref(id string) string {
	e.t.Helper()
	raw, err := e.svc.ExportConfig(e.t.Context(), e.scope, id)
	if err != nil {
		return id
	}
	return Ref(configName(raw), id)
}

// svcLaunched is forum_launch's reply for run n of the forum named ref.
func svcLaunched(ref string, n int) string {
	return fmt.Sprintf("Forum %s launched (run %d). You will be notified when it finishes; end your turn instead of checking status.", ref, n)
}

// svcStuck records Host.OnStuck calls.
type svcStuck struct {
	mu    sync.Mutex
	calls []string // "<forum id> <origin agent>: <error>"
}

func (s *svcStuck) record(id string, _ int, origin Origin, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, id+" "+origin.AgentID+": "+err.Error())
}

func (s *svcStuck) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// svcSetup builds a service over fakes with the fake controller. The
// workspace holds doc.md for svcConfigJSON's file source.
func svcSetup(t *testing.T) *svcEnv {
	t.Helper()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "doc.md"), []byte("# Doc\n\nBody.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(ws, "forums")
	e := &svcEnv{
		t:         t,
		agents:    svcNewAgents(),
		notifier:  &svcNotifier{base: base},
		logger:    &svcLogger{},
		ctrls:     &svcCtrls{byID: map[string]*svcCtrl{}},
		workspace: ws,
		scope:     Scope{AgentID: "launcher", BaseDirectory: base},
		stuck:     &svcStuck{},
	}
	e.svc = e.newService()
	return e
}

// newService builds another service over the same fakes and base, as a
// restarted process would.
func (e *svcEnv) newService() *Service {
	svc := New(Host{
		Messenger: svcMessenger{},
		Agents:    e.agents,
		Notifier:  e.notifier,
		Logger:    e.logger,
		Schemas:   JSONSchemaValidator{},
		OnStuck:   e.stuck.record,
	})
	svc.openCtrl = e.ctrls.open
	e.t.Cleanup(func() { svcClose(e.t, svc) })
	return svc
}

// restart closes the current service and replaces it with a new one,
// without running Recover.
func (e *svcEnv) restart() {
	e.t.Helper()
	svcClose(e.t, e.svc)
	e.svc = e.newService()
}

func svcClose(t *testing.T, svc *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		t.Errorf("close: %v", err)
	}
}

func (e *svcEnv) opts() LaunchOptions {
	return LaunchOptions{
		Scope:  e.scope,
		Origin: Origin{Channel: "test", ChatID: "chat-1"},
		ResolveFile: func(ref string) (string, error) {
			if filepath.IsAbs(ref) {
				return ref, nil
			}
			return filepath.Join(e.workspace, ref), nil
		},
		ReadAllowed: func(abs string) error {
			if !strings.HasPrefix(abs, e.workspace+string(filepath.Separator)) {
				return errors.New("outside the workspace")
			}
			return nil
		},
	}
}

// launch launches cfg (svcConfigJSON when empty) and waits for its
// controller to run.
func (e *svcEnv) launch(cfg string) (string, *svcCtrl) {
	e.t.Helper()
	if cfg == "" {
		cfg = svcConfigJSON
	}
	id, err := svcLaunch(e.t, e.svc, cfg, e.opts())
	if err != nil {
		e.t.Fatalf("launch: %v", err)
	}
	return id, e.ctrls.get(e.t, id)
}

func (e *svcEnv) status(id string) Status {
	e.t.Helper()
	sum, err := e.svc.Status(e.t.Context(), e.scope, id, 0)
	if err != nil {
		e.t.Fatalf("status %s: %v", id, err)
	}
	return sum.Status
}

// settled waits until the forum has no live run in the service and has
// the wanted status.
func (e *svcEnv) settled(id string, want Status) {
	e.t.Helper()
	svcEventually(e.t, fmt.Sprintf("forum %s settles %s", id, want), func() bool {
		if _, ok := e.svc.running(e.scope, id); ok {
			return false
		}
		return e.status(id) == want
	})
}

// running waits until the forum has a live run with status running.
func (e *svcEnv) running(id string) {
	e.t.Helper()
	svcEventually(e.t, "forum "+id+" running", func() bool {
		r, ok := e.svc.running(e.scope, id)
		return ok && r.ctrl.State().Status == StatusRunning
	})
}

// keptAlive reports whether the service keeps the forum's agents alive.
func (e *svcEnv) keptAlive(id string) bool {
	e.svc.mu.Lock()
	defer e.svc.mu.Unlock()
	_, ok := e.svc.paused[id]
	return ok
}

// store opens the store of a forum's latest run directly (the forum's
// handle when it has no run).
func (e *svcEnv) store(id string) *Store {
	e.t.Helper()
	s, err := OpenStore(e.scope.BaseDirectory, id)
	if err != nil {
		e.t.Fatalf("open store %s: %v", id, err)
	}
	n, err := latestRun(s)
	if err != nil {
		e.t.Fatalf("runs of %s: %v", id, err)
	}
	if n == 0 {
		return s
	}
	return s.Run(n)
}

// appendCommits writes commits to a forum that has no live controller, as
// a controller in a process that then died would have.
func (e *svcEnv) appendCommits(id string, commits ...Commit) {
	e.t.Helper()
	s := e.store(id)
	if err := s.Lock(); err != nil {
		e.t.Fatalf("lock %s: %v", id, err)
	}
	defer s.Unlock()
	for i := range commits {
		if _, err := nextAppend(s, &commits[i]); err != nil {
			e.t.Fatalf("append %s to %s: %v", commits[i].Kind, id, err)
		}
	}
}

// marker reports whether a cleanup marker of a forum's run 1 exists and
// its content.
func (e *svcEnv) marker(id, name string) (string, bool) {
	e.t.Helper()
	return e.markerRun(id, 1, name)
}

// markerRun is marker for run n.
func (e *svcEnv) markerRun(id string, n int, name string) (string, bool) {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.scope.BaseDirectory, dirCleanup, fmt.Sprintf("%s.%d.%s", id, n, name)))
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return string(data), true
}

// forumIDs lists the forums on disk.
func (e *svcEnv) forumIDs() []string {
	e.t.Helper()
	ids, err := ListForums(e.scope.BaseDirectory)
	if err != nil {
		e.t.Fatal(err)
	}
	return ids
}

// svcEventually polls cond for up to five seconds.
func svcEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// svcLaunch creates a forum holding cfg in opts.Scope and launches it. It
// returns the forum's ID whether or not the launch succeeded.
func svcLaunch(t *testing.T, svc *Service, cfg string, opts LaunchOptions) (string, error) {
	t.Helper()
	id, err := svc.NewForum(t.Context(), opts.Scope)
	if err != nil {
		t.Fatalf("new forum: %v", err)
	}
	if err = svc.SetConfig(t.Context(), opts.Scope, id, []byte(cfg)); err != nil {
		t.Fatalf("set the forum's configuration: %v", err)
	}
	_, err = svc.Launch(t.Context(), id, opts)
	return id, err
}
