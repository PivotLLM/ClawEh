// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
)

// failReads makes mail_read fail for the ids of bad and answer for the rest.
func (e *env) failReads(bad ...string) {
	e.tools.set("mail_read", func(args map[string]any) (string, error) {
		id := fmt.Sprint(args["id"])
		if slices.Contains(bad, id) {
			return "", errBoom
		}
		return fmt.Sprintf(`{"from":{"address":"news@example.org"},"subject":"About %s","body":{"content":"x"}}`, id), nil
	})
}

// reads are the ids mail_read was called for from call from on, in order.
func (e *env) reads(from int) []string {
	calls := e.tools.callsOf("mail_read")[from:]
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, fmt.Sprint(c.args["id"]))
	}
	return out
}

// A run takes new items before failed ones, so items that keep failing at
// the head of the source never stop it reaching the new ones.
func TestRunTakesNewItemsBeforeRetries(t *testing.T) {
	e := newEnv(t)
	e.setInbox([]string{"bad1", "bad2", "ok1"})
	e.failReads("bad1", "bad2")
	def := definition()
	def["limits"] = map[string]any{"stop_after_failures": 2}
	e.create(def)
	if rec := e.run("inbox-priority"); rec.Status != RunError || rec.Acted != 0 {
		t.Fatalf("first run = %+v", rec) // the two failures at the head stop it, as before
	}

	// The second run takes ok1 (new) first, then the two retries.
	calls := len(e.tools.callsOf("mail_read"))
	rec := e.run("inbox-priority")
	if got := e.reads(calls); !slices.Equal(got, []string{"ok1", "bad1", "bad2"}) {
		t.Errorf("order = %v", got)
	}
	if rec.Acted != 1 || rec.Failed != 2 {
		t.Errorf("second run = %+v", rec)
	}
}

// A failing item is tried limits.max_attempts times, counted only in runs
// where something else worked; then it is left alone, shown in the status,
// and tried again once the agent changes the definition.
func TestMaxAttempts(t *testing.T) {
	e := newEnv(t)
	inbox := []string{"bad", "ok1"}
	e.setInbox(inbox)
	e.failReads("bad")
	def := definition()
	def["limits"] = map[string]any{"max_attempts": 2}
	e.create(def)

	next := 1
	run := func() RunRecord {
		// A new item each run, so each run has one that works.
		next++
		inbox = append(inbox, fmt.Sprintf("ok%d", next))
		e.setInbox(inbox)
		return e.run("inbox-priority")
	}
	for i := 1; i <= 2; i++ {
		if rec := run(); rec.Failed != 1 || rec.Status != RunOK {
			t.Fatalf("run %d = %+v", i, rec)
		}
		if got := e.processed("inbox-priority")["bad"].attempts; got != i {
			t.Fatalf("attempts after run %d = %d", i, got)
		}
	}
	if e.log.warnings("1 item(s) failed 2 times and are not tried again") != 1 {
		t.Errorf("giving up was not logged once: %v", e.log.warns)
	}
	if st := e.status(); st.GaveUp != 1 || st.DryRunGaveUp != 0 {
		t.Errorf("status gave_up = %d, dry %d", st.GaveUp, st.DryRunGaveUp)
	}
	calls := len(e.tools.callsOf("mail_read"))
	if rec := run(); rec.Failed != 0 || slices.Contains(e.reads(calls), "bad") {
		t.Fatalf("an item given up on was tried again: %+v, reads %v", rec, e.reads(calls))
	}

	// Any change by the agent tries it again, from the first attempt.
	if err := e.svc.Update(context.Background(), e.scope, "inbox-priority", []byte(`{"activity_hours":48}`)); err != nil {
		t.Fatal(err)
	}
	if st := e.status(); st.GaveUp != 0 {
		t.Errorf("gave_up after an update = %d", st.GaveUp)
	}
	e.failReads()
	if rec := run(); rec.Acted != 2 {
		t.Fatalf("run after the update = %+v", rec)
	}
	if row := e.processed("inbox-priority")["bad"]; row.status != StatusActed {
		t.Errorf("bad after the fix = %+v", row)
	}
}

// A run in which nothing worked counts no attempt: an outage does not use
// up its items' attempts, however long it lasts.
func TestOutageCountsNoAttempts(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["limits"] = map[string]any{"max_attempts": 2}
	e.create(def)
	e.tools.set("mail_read", func(map[string]any) (string, error) { return "", errBoom })
	for range 4 {
		if rec := e.run("inbox-priority"); rec.Failed != 3 {
			t.Fatalf("run = %+v", rec)
		}
	}
	for id, row := range e.processed("inbox-priority") {
		if row.attempts != 0 {
			t.Errorf("%s has %d attempts after an outage", id, row.attempts)
		}
	}
	e.failReads()
	if rec := e.run("inbox-priority"); rec.Acted != 3 {
		t.Errorf("run after the outage = %+v", rec)
	}
}

// A model that cannot be used fails its item without an attempt, even in a
// run where other items worked.
func TestCooldownCountsNoAttempt(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["limits"] = map[string]any{"max_attempts": 1}
	e.create(def)
	n := 0
	e.model.setReply(func(string, string) (string, error) {
		if n++; n == 2 {
			return "", ErrNoModel
		}
		return "low", nil
	})
	rec := e.run("inbox-priority")
	if rec.Status != RunError || rec.Acted != 1 || rec.Failed != 1 {
		t.Fatalf("run = %+v", rec)
	}
	if row := e.processed("inbox-priority")["m2"]; row.status != StatusFailed || row.attempts != 0 {
		t.Errorf("m2 = %+v", row)
	}
}

// A dry run's failed items are counted and given up on the same way, apart
// from the live ones.
func TestDryRunMaxAttempts(t *testing.T) {
	e := newEnv(t)
	inbox := []string{"bad", "ok1"}
	e.setInbox(inbox)
	e.failReads("bad")
	def := definition()
	def["dry_run"] = true
	def["limits"] = map[string]any{"max_attempts": 2}
	e.create(def)
	for i := 2; i <= 3; i++ {
		inbox = append(inbox, fmt.Sprintf("ok%d", i))
		e.setInbox(inbox)
		if rec := e.run("inbox-priority"); rec.Failed != 1 {
			t.Fatalf("dry run = %+v", rec)
		}
	}
	if st := e.status(); st.DryRunGaveUp != 1 || st.GaveUp != 0 {
		t.Fatalf("status gave_up = %d, dry %d", st.GaveUp, st.DryRunGaveUp)
	}
	inbox = append(inbox, "ok4")
	e.setInbox(inbox)
	calls := len(e.tools.callsOf("mail_read"))
	if rec := e.run("inbox-priority"); rec.Failed != 0 || slices.Contains(e.reads(calls), "bad") {
		t.Fatalf("a dry-run item given up on was tried again: %+v", rec)
	}

	// Once it works its failure is forgotten; going live starts afresh.
	if err := e.svc.Update(context.Background(), e.scope, "inbox-priority", []byte(`{"activity_hours":48}`)); err != nil {
		t.Fatal(err)
	}
	e.failReads()
	if rec := e.run("inbox-priority"); rec.Classified != 1 {
		t.Fatalf("dry run after the fix = %+v", rec)
	}
	st, err := e.svc.state(context.Background(), e.scope.BaseDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if failed, err := st.dryFailed(context.Background(), "inbox-priority"); err != nil || len(failed) != 0 {
		t.Errorf("dry_failed after the fix = %v, %v", failed, err)
	}
}

// A state.db written before attempts were limited has its counts set to 0
// once: every failure was counted then, outages included.
func TestMigrateAttempts(t *testing.T) {
	ctx := context.Background()
	base := filepath.Join(t.TempDir(), "classifiers")
	open := func() *stateDB {
		t.Helper()
		st, err := openState(ctx, base, 0o750, 0o640)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	attempts := func(st *stateDB) int {
		t.Helper()
		rows, err := st.processed(ctx, "c")
		if err != nil {
			t.Fatal(err)
		}
		return rows["m1"].attempts
	}
	st := open()
	if err := st.setFailed(ctx, "c", "m1", "boom", testStart); err != nil {
		t.Fatal(err)
	}
	if err := st.addAttempts(ctx, tableProcessed, "c", []string{"m1"}); err != nil {
		t.Fatal(err)
	}
	// Back to the earlier schema: no listing columns on runs.
	for _, column := range addedRunColumns {
		if _, err := st.db.ExecContext(ctx, `ALTER TABLE runs DROP COLUMN `+column.name); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.close(); err != nil {
		t.Fatal(err)
	}

	st = open()
	if got := attempts(st); got != 0 {
		t.Errorf("attempts after the upgrade = %d, want 0", got)
	}
	if err := st.addAttempts(ctx, tableProcessed, "c", []string{"m1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.close(); err != nil {
		t.Fatal(err)
	}
	st = open()
	defer closeQuietly(st.db)
	if got := attempts(st); got != 1 {
		t.Errorf("attempts after a later open = %d, want 1 (reset once only)", got)
	}
	// A last run stored before the listing columns existed is still read.
	if _, err := st.db.ExecContext(ctx, `INSERT INTO runs (classifier, started_at, ended_at, status, listed, new_items, classified, acted, unclassified, failed, dry_run)
VALUES ('old', 1, 2, 'ok', 3, 3, 3, 3, 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if r, err := lastRunFrom(ctx, st.db, "old"); err != nil || r == nil || r.Listed != 3 || r.Pages != 0 || r.Listing != "" {
		t.Errorf("old run = %+v, %v", r, err)
	}
}
