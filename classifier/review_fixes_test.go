// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/toolspec"
)

// failClassifiedWrites makes every write of a classification to the
// processed table fail, as a full disk would.
func (e *env) failClassifiedWrites() {
	e.t.Helper()
	e.log.expectErrors()
	st, err := e.svc.state(context.Background(), e.scope.BaseDirectory)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER no_classified BEFORE INSERT ON processed WHEN NEW.status = 'classified'
BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		e.t.Fatal(err)
	}
}

// An item whose classification cannot be stored fails with an operator
// cause: a streak of them names it and advises telling the user, and its
// action is never run.
func TestStateWriteFailureHasAnOperatorCause(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["limits"] = map[string]any{"stop_after_failures": 3}
	e.create(def)
	e.failClassifiedWrites()
	rec := e.run("inbox-priority")
	const want = "stopped after 3 consecutive item failures: the classifier's state could not be written; the action was not run"
	if rec.Status != RunError || rec.Error != want {
		t.Fatalf("run = %+v, want %q", rec, want)
	}
	if agent, _ := e.notices(); len(agent) != 1 || !strings.HasSuffix(agent[0], "Only the operator can fix this; tell the user.\n"+
		fakeWrap("the classifier's state could not be written; the action was not run")) {
		t.Errorf("notice = %q, want the operator advice", agent)
	}
	if moved := e.moves(); len(moved) != 0 {
		t.Errorf("actions ran: %v", moved)
	}
}

// Every way an item fails gives it a cause with a summary: a run's error
// and its notice never name an empty cause.
func TestFailedItemsHaveACause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(e *env, def map[string]any)
		kind  causeKind
	}{
		{"fetch argument", func(_ *env, def map[string]any) {
			obj(def["fetch"])["args"] = map[string]any{"id": "{{item.nothing}}"}
		}, causeDefinition},
		{"fetch tool", func(e *env, _ map[string]any) {
			e.tools.set("mail_read", func(map[string]any) (string, error) { return "", errors.New("gone") })
		}, causeTool},
		{"fetch tool missing", func(e *env, _ map[string]any) {
			e.tools.set("mail_read", func(map[string]any) (string, error) {
				return "", ToolUnavailable("tool mail_read is not available to agent alice")
			})
		}, causeToolMissing},
		{"no fetch field", func(e *env, _ map[string]any) {
			e.tools.set("mail_read", func(map[string]any) (string, error) { return "plain text", nil })
		}, causeDefinition},
		{"model", func(e *env, _ map[string]any) {
			e.model.setReply(func(string, string) (string, error) { return "", errors.New("model cheap: 500") })
		}, causeTool},
		{"no model", func(e *env, _ map[string]any) {
			e.model.setReply(func(string, string) (string, error) { return "", ErrNoModel })
		}, causeNoModel},
		{"action argument", func(_ *env, def map[string]any) {
			obj(obj(def["actions"])["urgent"])["args"] = map[string]any{"id": "{{item.nothing}}"}
		}, causeDefinition},
		{"action tool", func(e *env, _ map[string]any) {
			e.tools.set("mail_move", func(map[string]any) (string, error) { return "", errors.New("boom") })
		}, causeTool},
		{"state write", func(e *env, _ map[string]any) { e.failClassifiedWrites() }, causeOperator},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			def := definition()
			created := false
			if tc.name != "action argument" && tc.name != "fetch argument" {
				e.create(def)
				created = true
			}
			tc.setup(e, def)
			if !created {
				e.create(def)
			}
			d := e.def()
			in, err := e.svc.loadInstructions(e.scope, d)
			if err != nil {
				t.Fatal(err)
			}
			items, err := listItems(context.Background(), d, "alice", e.tools)
			if err != nil {
				t.Fatal(err)
			}
			st, err := e.svc.state(context.Background(), e.scope.BaseDirectory)
			if err != nil {
				t.Fatal(err)
			}
			got := e.svc.handleItem(context.Background(), e.scope, d, in, st, items[0], nil, modeLive)
			if got.Outcome != OutcomeFailed || got.cause.summary == "" || got.cause.kind != tc.kind {
				t.Errorf("decision = %+v, cause %+v; want failed with a %v cause", got.Decision, got.cause, tc.kind)
			}
		})
	}
}

// A classifier whose errors are not reported does not report a state that
// cannot be opened and does not start the folder's mute: another
// classifier of the agent with errors on still reports it.
func TestStateMuteStartedOnlyByAReportedClassifier(t *testing.T) {
	e := newEnv(t)
	e.log.expectErrors()
	quiet := definition()
	quiet["id"] = "quiet"
	quiet["report"] = map[string]any{"errors": false}
	e.create(quiet)
	e.create(definition())
	quietDef, err := e.svc.validDefinition(e.scope, "quiet")
	if err != nil {
		t.Fatal(err)
	}
	loud := e.def()
	e.breakState()
	e.svc.runClassifier(context.Background(), e.scope, quietDef, nil)
	if n := len(e.notifier.allNotices()) + len(e.notifier.allAlerts()) + len(e.log.errors()); n != 0 {
		t.Fatalf("a classifier with errors off reported the state: %d notices, alerts or error lines", n)
	}
	e.svc.runClassifier(context.Background(), e.scope, loud, nil)
	if len(e.notifier.allNotices()) != 1 || len(e.notifier.allAlerts()) != 1 {
		t.Errorf("notices %d, alerts %d; want the reported classifier to report it once", len(e.notifier.allNotices()), len(e.notifier.allAlerts()))
	}
}

// breakState replaces the folder's state.db with a folder, on a new
// service (as after a restart), so it cannot be opened.
func (e *env) breakState() {
	e.t.Helper()
	if err := e.svc.Close(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	statePath := filepath.Join(e.scope.BaseDirectory, "state.db")
	for _, side := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(statePath + side); err != nil && !errors.Is(err, os.ErrNotExist) {
			e.t.Fatal(err)
		}
	}
	if err := os.Mkdir(statePath, 0o700); err != nil {
		e.t.Fatal(err)
	}
	e.svc = New(e.svc.host)
}

// The state problem Check Up shows ages out once nothing has met it for
// ErrorMute (its classifiers were deleted or turned off).
func TestLastStateErrorAgesOut(t *testing.T) {
	e := newEnv(t)
	e.log.expectErrors()
	e.create(definition())
	def := e.def()
	e.breakState()
	e.svc.runClassifier(context.Background(), e.scope, def, nil)
	if _, ok := LastStateError(e.scope.BaseDirectory, e.clock.Now().Add(ErrorMute)); !ok {
		t.Error("the problem is gone within ErrorMute")
	}
	if p, ok := LastStateError(e.scope.BaseDirectory, e.clock.Now().Add(ErrorMute+time.Second)); ok {
		t.Errorf("a problem nothing met for over ErrorMute = %+v", p)
	}
}

// A rerun cut short during its action says the classification was saved
// and the next run runs the action (it is stored with its action pending);
// one cut short before anything was stored says nothing was recorded.
func TestRerunInterruptedDuringAction(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	ctx, cancel := context.WithCancel(context.Background())
	e.tools.set("mail_move", func(map[string]any) (string, error) {
		cancel()
		return "", context.Canceled
	})
	rerun := toolByName(t, Tools(e.svc, fakeToolHost{scope: e.scope}), "rerun")
	res, err := rerun.Handler(&toolspec.ToolCall{Ctx: ctx, AgentID: "alice", Args: map[string]any{"id": "inbox-priority", "item_id": "m1"}})
	const want = "The classification was saved, but the classifier stopped before running its action; the next run will run it."
	if err != nil || !res.IsError || res.ForLLM != want {
		t.Fatalf("classifier_rerun = %+v, %v; want %q", res, err, want)
	}
	if row, ok := e.processed("inbox-priority")["m1"]; !ok || row.status != StatusClassified || row.category != "urgent" {
		t.Errorf("m1 = %+v, want classified with its action pending", row)
	}
	e.tools.set("mail_move", func(map[string]any) (string, error) { return "moved", nil })
	if rec := e.run("inbox-priority"); rec.Acted == 0 || e.moves()["m1"] != "Urgent" {
		t.Errorf("next run = %+v, moves %v; want m1's action run", rec, e.moves())
	}
}

// The detail of a stop_after_failures notice is the error of the latest
// item with the cause the run names, not of the last item when that one
// failed otherwise.
func TestStopDetailMatchesTheCause(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["limits"] = map[string]any{"stop_after_failures": 3}
	e.create(def)
	e.tools.set("mail_move", func(args map[string]any) (string, error) {
		return "", fmt.Errorf("boom %v", args["id"])
	})
	read := e.tools.handlers["mail_read"]
	e.tools.set("mail_read", func(args map[string]any) (string, error) {
		if args["id"] == "m3" {
			return "", errors.New("unreadable")
		}
		return read(args)
	})
	rec := e.run("inbox-priority")
	if want := "stopped after 3 consecutive item failures: action mail_move: the tool failed"; rec.Error != want {
		t.Fatalf("run error = %q, want %q", rec.Error, want)
	}
	if rows := e.runFailures(); len(rows) != 1 || rows[0].Detail != "action mail_move: the tool failed: boom m2" {
		t.Errorf("run_failed rows = %+v, want m2's action error as the detail", rows)
	}
	if agent, _ := e.notices(); len(agent) != 1 || !strings.HasSuffix(agent[0], fakeWrap("action mail_move: the tool failed: boom m2")) {
		t.Errorf("notice = %q", agent)
	}
}
