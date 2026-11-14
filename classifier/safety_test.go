// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A classification that cannot be recorded fails the item before its action
// runs: a later run, not knowing the action ran, would run it again.
func TestRunDoesNotActWhenClassificationIsNotRecorded(t *testing.T) {
	e := newEnv(t)
	e.log.expectErrors()
	e.setInbox([]string{"m1"})
	e.create(definition())
	st, err := e.svc.state(context.Background(), e.scope.BaseDirectory)
	if err != nil {
		t.Fatal(err)
	}
	e.model.setReply(func(string, string) (string, error) {
		// The processed table disappears between the model's answer and
		// the write that records it.
		if _, err := st.db.ExecContext(context.Background(), `DROP TABLE processed`); err != nil {
			t.Error(err)
		}
		return "urgent", nil
	})
	rec := e.run("inbox-priority")
	if rec.Failed != 1 || rec.Acted != 0 {
		t.Errorf("run = %+v", rec)
	}
	if n := len(e.tools.callsOf("mail_move")); n != 0 {
		t.Errorf("the action ran %d times without its classification recorded", n)
	}
	if len(e.log.errors()) == 0 {
		t.Error("the failed write was not logged")
	}
}

// State is written even when the run is stopped while the action runs, so
// an action that ran is never repeated.
func TestRunRecordsActionAfterCancel(t *testing.T) {
	e := newEnv(t)
	e.setInbox([]string{"m1"})
	e.create(definition())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.tools.set("mail_move", func(map[string]any) (string, error) {
		cancel() // shutdown arrives while the action runs; the action succeeds
		return "moved", nil
	})
	rec := e.svc.runClassifier(ctx, e.scope, e.def(), nil)
	if rec.Acted != 1 {
		t.Fatalf("run = %+v", rec)
	}
	if got := e.processed("inbox-priority")["m1"].status; got != StatusActed {
		t.Errorf("status after a cancelled run = %q, want acted", got)
	}
	e.tools.set("mail_move", func(map[string]any) (string, error) { return "moved", nil })
	e.run("inbox-priority")
	if n := len(e.tools.callsOf("mail_move")); n != 1 {
		t.Errorf("the action ran %d times, want once", n)
	}
}

// Marking an item acted needs its row: no row is an error, not a silent
// no-op.
func TestSetActedNeedsTheRow(t *testing.T) {
	e := newEnv(t)
	st, err := e.svc.state(context.Background(), e.scope.BaseDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.setActed(context.Background(), "x", "missing", testStart); err == nil {
		t.Error("setActed without a row succeeded")
	}
	if err := st.setClassified(context.Background(), "x", "m1", Decision{Category: "urgent", Model: "cheap"}, testStart); err != nil {
		t.Fatal(err)
	}
	if err := st.setActed(context.Background(), "x", "m1", testStart); err != nil {
		t.Errorf("setActed = %v", err)
	}
}

// Delete waits for the run in progress; nothing it writes survives the
// delete.
func TestDeleteWaitsForTheRun(t *testing.T) {
	e := newEnv(t)
	e.setInbox([]string{"m1", "m2"})
	e.create(definition())
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	e.model.setReply(func(string, string) (string, error) {
		entered <- struct{}{}
		<-release
		return "low", nil
	})
	e.start()
	<-entered
	deleted := make(chan error, 1)
	go func() { deleted <- e.svc.Delete(context.Background(), e.scope, "inbox-priority") }()
	select {
	case err := <-deleted:
		t.Fatalf("Delete returned during the run: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if rows := e.processed("inbox-priority"); len(rows) != 0 {
		t.Errorf("rows written by the run survived the delete: %v", rows)
	}
	if entries, err := e.svc.Activity(e.scope, "inbox-priority", ActivityQuery{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Activity after Delete = %v, %v", entries, err)
	}
	if n := e.model.count(); n != 1 {
		t.Errorf("model calls = %d, want only the item in flight", n)
	}
}

// Close waits for a rerun in progress before closing the state databases,
// and no rerun starts once Close has begun.
func TestCloseWaitsForRerun(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.run("inbox-priority")
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	e.model.setReply(func(string, string) (string, error) {
		entered <- struct{}{}
		<-release
		return "low", nil
	})
	reran := make(chan error, 1)
	go func() {
		_, err := e.svc.Rerun(context.Background(), e.scope, "inbox-priority", "m1")
		reran <- err
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- e.svc.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned during a rerun: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-reran; err != nil {
		t.Errorf("Rerun = %v", err)
	}
	if err := <-closed; err != nil {
		t.Errorf("Close = %v", err)
	}
	if _, err := e.svc.Rerun(context.Background(), e.scope, "inbox-priority", "m1"); !errors.Is(err, ErrClosed) {
		t.Errorf("Rerun after Close = %v, want ErrClosed", err)
	}
}

// Close with a deadline that ends before a run does leaves the databases
// open for it.
func TestCloseTimeoutKeepsDatabases(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	e.model.setReply(func(string, string) (string, error) {
		entered <- struct{}{}
		<-release
		return "low", nil
	})
	reran := make(chan error, 1)
	go func() {
		_, err := e.svc.Rerun(context.Background(), e.scope, "inbox-priority", "m2")
		reran <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := e.svc.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close = %v", err)
	}
	close(release)
	if err := <-reran; err != nil && !strings.Contains(err.Error(), "closed") {
		t.Errorf("Rerun = %v", err)
	}
	if got := e.processed("inbox-priority")["m2"].status; got != StatusActed {
		t.Errorf("the rerun's write was lost: status %q", got)
	}
}
