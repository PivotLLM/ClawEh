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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Test helpers of seam (d), prefixed ctl so they cannot collide with the
// helpers of the other seams in the same package.

// ctlCall is one Ask the fake messenger received, with the header of the
// controller's message parsed.
type ctlCall struct {
	Agent       string
	Participant string
	Message     string
	Wait        time.Duration
	Layer       string
	Round       int
	Moderator   bool
	Repair      bool
}

var ctlHeader = regexp.MustCompile(`^Forum "[^"]*", layer "([^"]+)", (?:round (\d+)|moderator check after round (\d+))\.`)

// ctlMessenger is a scripted Messenger: respond decides each reply; hook,
// when set, runs first (to block, observe or fail a call).
type ctlMessenger struct {
	mu      sync.Mutex
	calls   []ctlCall
	agents  map[string]string // agent ID -> participant ID
	respond func(ctlCall) (Reply, error)
	hook    func(ctx context.Context, cl ctlCall) error
}

func (m *ctlMessenger) Ask(ctx context.Context, agentID, message string, wait time.Duration) (Reply, error) {
	cl := ctlCall{Agent: agentID, Message: message, Wait: wait, Repair: strings.Contains(message, headingRejected)}
	m.mu.Lock()
	cl.Participant = m.agents[agentID]
	m.mu.Unlock()
	if g := ctlHeader.FindStringSubmatch(message); g != nil {
		cl.Layer = g[1]
		digits := g[2]
		if digits == "" {
			digits, cl.Moderator = g[3], true
		}
		round, err := strconv.Atoi(digits)
		if err != nil {
			return Reply{}, err
		}
		cl.Round = round
	}
	m.mu.Lock()
	m.calls = append(m.calls, cl)
	hook, respond := m.hook, m.respond
	m.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, cl); err != nil {
			return Reply{}, err
		}
	}
	return respond(cl)
}

// all returns a copy of every call so far.
func (m *ctlMessenger) all() []ctlCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

// find returns the calls to one participant in one layer and round
// (moderator checks when moderator is true).
func (m *ctlMessenger) find(pid, layer string, round int, moderator bool) []ctlCall {
	var out []ctlCall
	for _, cl := range m.all() {
		if cl.Participant == pid && cl.Layer == layer && cl.Round == round && cl.Moderator == moderator {
			out = append(out, cl)
		}
	}
	return out
}

// ctlAgents is a fake registry: every agent exists unless listed in gone.
type ctlAgents struct {
	mu   sync.Mutex
	gone map[string]bool
}

func (a *ctlAgents) Exists(_ context.Context, id string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.gone[id], nil
}
func (a *ctlAgents) MayTarget(context.Context, string, string) (bool, error) { return true, nil }
func (a *ctlAgents) Models(context.Context, string) ([]ModelInfo, error)     { return nil, nil }
func (a *ctlAgents) CreateClone(context.Context, CloneSpec) (string, error) {
	return "", errors.New("not used")
}

func (a *ctlAgents) CreateFresh(context.Context, FreshSpec) (string, error) {
	return "", errors.New("not used")
}
func (a *ctlAgents) Delete(context.Context, string, string) error { return nil }
func (a *ctlAgents) Touch(context.Context, string, string) error  { return nil }

// ctlLogger records log lines; it never calls t.Log (runs may outlive a
// subtest's logging window).
type ctlLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *ctlLogger) addf(level, format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, level+" "+fmt.Sprintf(format, v...))
}
func (l *ctlLogger) Debugf(f string, v ...any) { l.addf("DEBUG", f, v...) }
func (l *ctlLogger) Infof(f string, v ...any)  { l.addf("INFO", f, v...) }
func (l *ctlLogger) Warnf(f string, v ...any)  { l.addf("WARN", f, v...) }
func (l *ctlLogger) Errorf(f string, v ...any) { l.addf("ERROR", f, v...) }

// ctlForum is one launched forum on disk with its fake host.
type ctlForum struct {
	t      *testing.T
	s      *Store
	cfg    *Config
	snap   *Snapshot
	msg    *ctlMessenger
	agents *ctlAgents
	log    *ctlLogger
	host   Host
}

// ctlConfig is a small forum: Alice as herself, Bob and a chair as fresh
// participants, one text source, generous limits; the caller supplies the
// layers.
func ctlConfig(layers ...Layer) *Config {
	return &Config{
		Version: ConfigVersion,
		Name:    "ctl test",
		Brief:   Brief{Purpose: "BRIEF-PURPOSE", Task: "BRIEF-TASK", Constraints: []string{"BRIEF-CONSTRAINT"}},
		Sources: map[string]Source{"notes": {Decode: FormatText, Inline: json.RawMessage(`"SOURCE-NOTES"`)}},
		Participants: map[string]Participant{
			"alice": {Agent: "alice", Instructions: "PRIV-ALICE", Name: "Alice"},
			"bob":   {Model: "default", Instructions: "PRIV-BOB", Name: "Bob"},
			"chair": {Model: "default", Instructions: "PRIV-CHAIR"},
		},
		Layers: layers,
		Limits: Limits{MaxCalls: 100, MaxDurationSeconds: 600, CallTimeoutSeconds: 60, MaxAttemptsPerTurn: 3, MaxParallelCalls: 2},
	}
}

// ctlLayer is a layer of Alice and Bob reading the notes source.
func ctlLayer(id string, delivery Delivery, rounds int, format Format) Layer {
	return Layer{
		ID: id, Participants: []string{"alice", "bob"}, Instructions: "LAYER-" + id,
		Inputs: []Route{{From: "source:notes"}}, Delivery: delivery, MaxRounds: rounds,
		Output: Output{Format: format},
	}
}

// ctlOption adjusts a forum before it is written.
type ctlOption func(*ctlForum)

// ctlDeadline sets the snapshot deadline relative to now.
func ctlDeadline(d time.Duration) ctlOption {
	return func(f *ctlForum) { f.snap.Deadline = time.Now().Add(d) }
}

// ctlLaunch writes everything Launch writes for cfg (forum.json, sources,
// participants.json, snapshot.json, the launched commit) and returns the
// forum with a fake host whose messenger answers with ctlReply.
func ctlLaunch(t *testing.T, cfg *Config, opts ...ctlOption) *ctlForum {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ctlLaunchRaw(t, raw, opts...)
}

// ctlLaunchRaw is ctlLaunch for configuration text.
func ctlLaunchRaw(t *testing.T, raw []byte, opts ...ctlOption) *ctlForum {
	t.Helper()
	cfg, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err = ValidateStatic(cfg); err != nil {
		t.Fatalf("ValidateStatic: %v", err)
	}
	s := stNewStore(t)
	if err = s.Lock(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Unlock)
	if err = s.WriteConfig(raw); err != nil {
		t.Fatal(err)
	}
	sources := map[string]SourceRecord{}
	for id, src := range cfg.Sources {
		content := []byte(src.Inline)
		if src.Decode != FormatJSON {
			var text string
			if err = json.Unmarshal(src.Inline, &text); err != nil {
				t.Fatal(err)
			}
			content = []byte(text)
		}
		if sources[id], err = s.WriteSource(id, src.Decode, content); err != nil {
			t.Fatal(err)
		}
	}
	parts := &Participants{Participants: map[string]ParticipantRecord{}}
	agents := map[string]string{}
	for id, p := range cfg.Participants {
		rec := ParticipantRecord{ID: id, Form: p.Form(), Name: p.Name, Model: p.Model}
		if rec.Name == "" {
			rec.Name = id
		}
		switch rec.Form {
		case FormExisting:
			rec.AgentID = p.Agent
		case FormClone, FormFresh:
			rec.AgentID, rec.Created = "tmp-"+id, true
		}
		if rec.Form == FormFresh {
			rec.Mode = p.Mode
			if rec.Mode == "" {
				rec.Mode = FreshModeMemory
			}
		}
		parts.Participants[id] = rec
		agents[rec.AgentID] = id
	}
	if err = s.WriteParticipants(parts); err != nil {
		t.Fatal(err)
	}
	modSchemas := map[string]json.RawMessage{}
	layers := make([]string, 0, len(cfg.Layers))
	for _, l := range cfg.EnabledLayers() {
		layers = append(layers, l.ID)
		if l.Moderator != nil {
			var assessment json.RawMessage
			if l.Moderator.Schema != "" {
				assessment = cfg.Schemas[l.Moderator.Schema]
			}
			if modSchemas[l.ID], err = EffectiveModeratorSchema(l, assessment); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed := int64(42)
	if cfg.Seed != nil {
		seed = *cfg.Seed
	}
	now := time.Now().UTC()
	f := &ctlForum{t: t, s: s, cfg: cfg, agents: &ctlAgents{gone: map[string]bool{}}, log: &ctlLogger{}}
	f.snap = &Snapshot{
		ForumID: s.ID(), Run: s.RunNumber(), Name: cfg.Name, LaunchedAt: now, BaseDirectory: s.base,
		Deadline: now.Add(time.Duration(cfg.Limits.MaxDurationSeconds) * time.Second), ConfigDigest: digest(raw),
		Seed: seed, Limits: cfg.Limits, Layers: layers, ResultLayers: cfg.EffectiveResultLayers(),
		Models: map[string]string{}, ModeratorSchemas: modSchemas, Sources: sources,
	}
	for _, o := range opts {
		o(f)
	}
	if err = s.WriteSnapshot(f.snap); err != nil {
		t.Fatal(err)
	}
	if _, err := nextAppend(s, &Commit{Kind: CommitLaunched}); err != nil {
		t.Fatal(err)
	}
	f.msg = &ctlMessenger{agents: agents}
	f.msg.respond = func(cl ctlCall) (Reply, error) { return f.reply(cl), nil }
	f.host = Host{Messenger: f.msg, Agents: f.agents, Notifier: nil, Logger: f.log, Schemas: JSONSchemaValidator{}}
	return f
}

// ctlMark is the default reply text of a participant turn: unique per
// participant, layer and round, so visibility is a substring check.
func ctlMark(pid, layer string, round int) string {
	return fmt.Sprintf("OUT[%s/%s/r%d]", pid, layer, round)
}

// reply is the default script: a valid reply for the layer's format
// (JSON outputs carry the mark in "claim" and a private "detail"), and
// CONTINUE from a moderator.
func (f *ctlForum) reply(cl ctlCall) Reply {
	if cl.Moderator {
		return Reply{Outcome: OutcomeOK, Text: `{"decision":"CONTINUE","reason":"carry on","guidance":null}`}
	}
	layer, _ := f.cfg.Layer(cl.Layer)
	mark := ctlMark(cl.Participant, cl.Layer, cl.Round)
	if layer.Output.Format == FormatJSON {
		return Reply{Outcome: OutcomeOK, Text: fmt.Sprintf(`{"claim":%q,"detail":"DETAIL-%s"}`, mark, mark)}
	}
	return Reply{Outcome: OutcomeOK, Text: mark}
}

// open opens the forum.
func (f *ctlForum) open() *Controller {
	f.t.Helper()
	c, err := Open(context.Background(), f.s, f.host)
	if err != nil {
		f.t.Fatalf("Open: %v", err)
	}
	return c
}

// run opens the forum and runs it, failing on an error.
func (f *ctlForum) run() (*Controller, Status) {
	f.t.Helper()
	c := f.open()
	st, err := c.Run(context.Background())
	if err != nil {
		f.t.Fatalf("Run: %v", err)
	}
	return c, st
}

// transcript returns transcript.md ("" when absent).
func (f *ctlForum) transcript() string {
	f.t.Helper()
	data, err := f.s.ReadFile(fileTranscript)
	if err != nil {
		return ""
	}
	return string(data)
}

// result reads result.json.
func (f *ctlForum) result() *Result {
	f.t.Helper()
	r, err := f.s.ReadResult()
	if err != nil {
		f.t.Fatalf("ReadResult: %v", err)
	}
	return r
}

// state is the replayed state of the log, checked against state.json.
func (f *ctlForum) state() *State {
	f.t.Helper()
	commits, err := f.s.ReadCommits()
	if err != nil {
		f.t.Fatal(err)
	}
	st, err := Replay(f.cfg, f.snap, commits)
	if err != nil {
		f.t.Fatalf("Replay: %v", err)
	}
	return st
}

// attempts lists one layer's attempts.
func (f *ctlForum) attempts(layer string) []AttemptRecord {
	f.t.Helper()
	a, err := f.s.ListAttempts(layer)
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

// ctlWant fails unless got equals want.
func ctlWant[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// ctlContains fails unless s contains every sub.
func ctlContains(t *testing.T, what, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Fatalf("%s lacks %q:\n%s", what, sub, s)
		}
	}
}

// ctlLacks fails if s contains any sub.
func ctlLacks(t *testing.T, what, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			t.Fatalf("%s contains %q:\n%s", what, sub, s)
		}
	}
}

// ctlOne returns the only call to a participant in a layer and round.
func (f *ctlForum) ctlOne(pid, layer string, round int, moderator bool) ctlCall {
	f.t.Helper()
	calls := f.msg.find(pid, layer, round, moderator)
	if len(calls) != 1 {
		f.t.Fatalf("calls to %s in %s round %d (moderator %v): %d, want 1", pid, layer, round, moderator, len(calls))
	}
	return calls[0]
}

// ctlJSON is the comparison form of v.
func ctlJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// resume records the resume of a paused forum (what the service commits
// before running it again) and runs it.
func (f *ctlForum) resume() (*Controller, Status) {
	f.t.Helper()
	if _, err := nextAppend(f.s, &Commit{Kind: CommitResumed}); err != nil {
		f.t.Fatalf("resume: %v", err)
	}
	return f.run()
}
