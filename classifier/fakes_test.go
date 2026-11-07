// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/internal/clock"
)

// fakeTools is the agent's tool registry: handlers by tool name, and a log of
// calls.
type fakeTools struct {
	mu       sync.Mutex
	handlers map[string]func(args map[string]any) (string, error)
	calls    []toolCall
}

type toolCall struct {
	agent, tool string
	args        map[string]any
}

func (f *fakeTools) Has(agentID, tool string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.handlers[tool]; !ok {
		return fmt.Errorf("tool %s is not available to agent %s", tool, agentID)
	}
	return nil
}

func (f *fakeTools) Call(_ context.Context, agentID, tool string, args map[string]any) (string, error) {
	f.mu.Lock()
	h, ok := f.handlers[tool]
	f.calls = append(f.calls, toolCall{agent: agentID, tool: tool, args: args})
	f.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("tool %s is not available to agent %s", tool, agentID)
	}
	return h(args)
}

func (f *fakeTools) set(tool string, h func(args map[string]any) (string, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[tool] = h
}

func (f *fakeTools) remove(tool string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.handlers, tool)
}

// callsOf returns the calls of tool, in order.
func (f *fakeTools) callsOf(tool string) []toolCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []toolCall
	for _, c := range f.calls {
		if c.tool == tool {
			out = append(out, c)
		}
	}
	return out
}

// fakeModel answers with reply(user) and counts its calls.
type fakeModel struct {
	mu      sync.Mutex
	reply   func(system, user string) (string, error)
	calls   []string // the user messages
	systems []string // the system prompts
}

func (m *fakeModel) Complete(_ context.Context, _, system, user string) (string, string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, user)
	m.systems = append(m.systems, system)
	reply := m.reply
	m.mu.Unlock()
	r, err := reply(system, user)
	return r, "cheap", err
}

func (m *fakeModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *fakeModel) setReply(f func(system, user string) (string, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reply = f
}

type fakeNotifier struct {
	mu    sync.Mutex
	texts []string
}

func (n *fakeNotifier) Summary(_ context.Context, _, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.texts = append(n.texts, text)
	return nil
}

func (n *fakeNotifier) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.texts...)
}

type fakeAuditor struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (a *fakeAuditor) Record(ev AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
}

func (a *fakeAuditor) kinds(kind string) []AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []AuditEvent
	for _, e := range a.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// testLogger keeps warnings for assertions.
type testLogger struct {
	t     *testing.T
	mu    sync.Mutex
	warns []string
}

func (l *testLogger) Debugf(string, ...any) {}
func (l *testLogger) Infof(string, ...any)  {}
func (l *testLogger) Warnf(format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(format, v...))
}

func (l *testLogger) Errorf(format string, v ...any) {
	l.t.Errorf("unexpected error log: "+format, v...)
}

func (l *testLogger) warnings(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, w := range l.warns {
		if strings.Contains(w, sub) {
			n++
		}
	}
	return n
}

// env is a service over fakes with one scope, alice's.
type env struct {
	t        *testing.T
	svc      *Service
	scope    Scope
	tools    *fakeTools
	model    *fakeModel
	notifier *fakeNotifier
	auditor  *fakeAuditor
	log      *testLogger
	clock    *clock.Fake
	ceilings Ceilings
	scopes   []Scope
	mu       sync.Mutex
}

var testStart = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)

// inbox is the source list: message ids, in order.
var inboxIDs = []string{"m1", "m2", "m3"}

// newEnv builds a service whose alice has a mail source (mail_list over
// inbox), a reader (mail_read) and a mover (mail_move), and a model that
// answers "urgent" for mail from bob@example.com and "low" otherwise.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		t:        t,
		scope:    Scope{AgentID: "alice", BaseDirectory: filepath.Join(t.TempDir(), "classifiers")},
		tools:    &fakeTools{handlers: map[string]func(map[string]any) (string, error){}},
		model:    &fakeModel{},
		notifier: &fakeNotifier{},
		auditor:  &fakeAuditor{},
		clock:    clock.NewFake(testStart),
		ceilings: Ceilings{MaxRules: 100, MaxPromptChars: 20000, MaxItemChars: 50000, MaxItemsPerRun: 500, MaxParallel: 8, MaxActivityHours: 720, ProcessedDays: 30},
	}
	e.log = &testLogger{t: t}
	e.scopes = []Scope{e.scope}
	e.setInbox(inboxIDs)
	e.tools.set("mail_read", func(args map[string]any) (string, error) {
		id := fmt.Sprint(args["id"])
		from := "news@example.org"
		if id == "m1" || id == "m3" {
			from = "bob@example.com"
		}
		return fmt.Sprintf(`{"from":{"address":%q},"subject":"About %s","body":{"content":"<p>Hello <b>Alice</b></p><script>x()</script>"}}`, from, id), nil
	})
	e.tools.set("mail_move", func(map[string]any) (string, error) { return "moved", nil })
	e.model.setReply(func(_, user string) (string, error) {
		if strings.Contains(user, "bob@example.com") {
			return "urgent\nfrom a client", nil
		}
		return "low: a newsletter", nil
	})
	e.svc = New(Host{
		Tools: e.tools, Model: e.model, Notifier: e.notifier, Auditor: e.auditor, Logger: e.log, Clock: e.clock,
		Modes:    func(string) (os.FileMode, os.FileMode) { return 0o750, 0o640 },
		Ceilings: func() Ceilings { e.mu.Lock(); defer e.mu.Unlock(); return e.ceilings },
		Scopes:   func() []Scope { e.mu.Lock(); defer e.mu.Unlock(); return append([]Scope(nil), e.scopes...) },
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.svc.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return e
}

// setMaxRules changes the install's max_rules.
func (e *env) setMaxRules(n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ceilings.MaxRules = n
}

// setInbox makes mail_list return messages with these ids.
func (e *env) setInbox(ids []string) {
	e.tools.set("mail_list", func(map[string]any) (string, error) {
		msgs := make([]map[string]any, len(ids))
		for i, id := range ids {
			msgs[i] = map[string]any{"id": id, "subject": "About " + id}
		}
		b, err := json.Marshal(map[string]any{"messages": msgs})
		return string(b), err
	})
}

// definition is the inbox classifier as a map, to change before creating.
func definition() map[string]any {
	return map[string]any{
		"id":      "inbox-priority",
		"enabled": true,
		"trigger": map[string]any{"every_seconds": 120},
		"source":  map[string]any{"tool": "mail_list", "args": map[string]any{"folder": "Inbox"}, "items": "messages", "id": "id"},
		"fetch": map[string]any{
			"tool":   "mail_read",
			"args":   map[string]any{"id": "{{item.id}}"},
			"fields": map[string]any{"from": "from.address", "subject": "subject", "body": "body.content"},
			"html":   []any{"body"},
		},
		"classify": map[string]any{
			"instructions": "Decide how important this email is to the user.",
			"rules":        []any{"Email from bob@example.com is from a client and is always urgent."},
			"categories": []any{
				map[string]any{"name": "urgent", "description": "Needs the user today."},
				map[string]any{"name": "low", "description": "Newsletters."},
			},
		},
		"actions": map[string]any{
			"urgent": map[string]any{"tool": "mail_move", "args": map[string]any{"id": "{{item.id}}", "destination": "Urgent"}},
			"low":    map[string]any{"tool": "mail_move", "args": map[string]any{"id": "{{item.id}}", "destination": "{{category}}"}},
		},
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// create saves def for alice.
func (e *env) create(def map[string]any) *Definition {
	e.t.Helper()
	if _, err := e.svc.Create(context.Background(), e.scope, mustJSON(e.t, def)); err != nil {
		e.t.Fatalf("Create: %v", err)
	}
	d, err := e.svc.validDefinition(e.scope, fmt.Sprint(def["id"]))
	if err != nil {
		e.t.Fatalf("read back: %v", err)
	}
	return d
}

// run runs the classifier once, as the scheduler would.
func (e *env) run(id string) RunRecord {
	e.t.Helper()
	d, err := e.svc.validDefinition(e.scope, id)
	if err != nil {
		e.t.Fatalf("definition %s: %v", id, err)
	}
	return e.svc.runClassifier(context.Background(), e.scope, d, nil)
}

// processed returns the classifier's processed rows by item id.
func (e *env) processed(id string) map[string]processedRow {
	e.t.Helper()
	st, err := e.svc.state(context.Background(), e.scope.BaseDirectory)
	if err != nil {
		e.t.Fatal(err)
	}
	rows, err := st.processed(context.Background(), NormalizeID(id))
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

// moves returns the destination of every mail_move call by message id.
func (e *env) moves() map[string]any {
	out := map[string]any{}
	for _, c := range e.tools.callsOf("mail_move") {
		out[fmt.Sprint(c.args["id"])] = c.args["destination"]
	}
	return out
}

var errBoom = errors.New("boom")

// obj is v as a JSON object; a test definition map always has one there.
func obj(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		panic(fmt.Sprintf("not a JSON object: %#v", v))
	}
	return m
}

// activity reads the inbox classifier's activity log.
func (e *env) activity(q ActivityQuery) []ActivityEntry {
	e.t.Helper()
	entries, err := e.svc.Activity(e.scope, "inbox-priority", q)
	if err != nil {
		e.t.Fatalf("Activity: %v", err)
	}
	return entries
}

// list lists scope's classifiers.
func (e *env) list(scope Scope) []Summary {
	e.t.Helper()
	sums, err := e.svc.List(scope)
	if err != nil {
		e.t.Fatalf("List: %v", err)
	}
	return sums
}

// def reads the inbox classifier's definition.
func (e *env) def() *Definition {
	e.t.Helper()
	d, err := e.svc.validDefinition(e.scope, "inbox-priority")
	if err != nil {
		e.t.Fatalf("definition: %v", err)
	}
	return d
}
