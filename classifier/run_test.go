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
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// list -> fetch -> classify -> act, end to end; a second run of the same
// list costs nothing.
func TestRunEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	rec := e.run("inbox-priority")

	if rec.Status != RunOK || rec.Listed != 3 || rec.NewItems != 3 || rec.Classified != 3 || rec.Acted != 3 || rec.Failed != 0 {
		t.Fatalf("run = %+v", rec)
	}
	want := map[string]any{"m1": "Urgent", "m2": "low", "m3": "Urgent"}
	if got := e.moves(); !reflect.DeepEqual(got, want) {
		t.Errorf("moves = %v, want %v", got, want)
	}
	for _, c := range e.tools.callsOf("mail_move") {
		if c.agent != "alice" {
			t.Errorf("action ran as %q, want alice", c.agent)
		}
	}
	// Every tool and model call names the classifier it is made for.
	for _, c := range e.tools.calls {
		if c.classifier != "inbox-priority" {
			t.Errorf("%s called for classifier %q, want inbox-priority", c.tool, c.classifier)
		}
	}
	for _, c := range e.model.classifiers {
		if c != "inbox-priority" {
			t.Errorf("model called for classifier %q, want inbox-priority", c)
		}
	}
	if got := e.tools.callsOf("mail_list")[0].args["folder"]; got != "Inbox" {
		t.Errorf("source args = %v", e.tools.callsOf("mail_list")[0].args)
	}
	for id, row := range e.processed("inbox-priority") {
		if row.status != StatusActed {
			t.Errorf("%s status = %s, want acted", id, row.status)
		}
	}
	if got := e.processed("inbox-priority")["m1"]; got.category != "urgent" || got.reason != "from a client" || got.model != "cheap" {
		t.Errorf("m1 row = %+v", got)
	}

	// The second run sees the same ids: no fetch, no model call, no action.
	calls, reads := e.model.count(), len(e.tools.callsOf("mail_read"))
	rec = e.run("inbox-priority")
	if rec.NewItems != 0 || e.model.count() != calls || len(e.tools.callsOf("mail_read")) != reads || len(e.tools.callsOf("mail_move")) != 3 {
		t.Errorf("second run = %+v; model calls %d -> %d", rec, calls, e.model.count())
	}

	// Activity: one entry per item, newest first, fields kept.
	entries, err := e.svc.Activity(e.scope, "inbox-priority", ActivityQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("activity = %d entries", len(entries))
	}
	for _, en := range entries {
		if en.Outcome != OutcomeActed || en.Action != "mail_move" || en.Fields["from"] == "" || en.DryRun || en.Rerun {
			t.Errorf("activity entry = %+v", en)
		}
	}
	if n := len(e.auditor.kinds(AuditClassification)); n != 3 {
		t.Errorf("classification audit rows = %d, want 3", n)
	}
	if n := len(e.auditor.kinds(AuditWrite)); n != 1 {
		t.Errorf("write audit rows = %d, want 1", n)
	}
	if got := e.notifier.all(); len(got) != 0 {
		t.Errorf("summaries on a clean run = %v", got)
	}

	// Everything created has exactly the configured modes.
	for path, want := range map[string]os.FileMode{
		e.scope.BaseDirectory: 0o750,
		filepath.Join(e.scope.BaseDirectory, "inbox-priority.json"): 0o640,
		filepath.Join(e.scope.BaseDirectory, "state.db"):            0o640,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", filepath.Base(path), got, want)
		}
	}
}

// The HTML of the fields fetch.html names is reduced to text before the
// model sees it; other fields are left as they are.
func TestRunReducesHTML(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.run("inbox-priority")
	user := e.model.calls[0]
	if !strings.Contains(user, "field \"body\"\nHello Alice\n") {
		t.Errorf("body not reduced to text:\n%s", user)
	}
	if strings.Contains(user, "<p>") || strings.Contains(user, "x()") {
		t.Errorf("tags or script reached the model:\n%s", user)
	}

	e2 := newEnv(t)
	def := definition()
	delete(obj(def["fetch"]), "html")
	e2.create(def)
	e2.run("inbox-priority")
	if !strings.Contains(e2.model.calls[0], "<p>Hello <b>Alice</b></p>") {
		t.Errorf("a field not named in fetch.html was changed:\n%s", e2.model.calls[0])
	}
}

// A crash (or failure) between classify and act repeats the item, never
// skips the action, and reuses the first category (design 7.1b).
func TestRunReplaysActionWithFirstCategory(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.tools.set("mail_move", func(map[string]any) (string, error) { return "", errBoom })
	rec := e.run("inbox-priority")
	if rec.Failed != 3 || rec.Acted != 0 || rec.Status != RunOK {
		t.Fatalf("failing run = %+v", rec)
	}
	rows := e.processed("inbox-priority")
	if rows["m1"].status != StatusFailed || rows["m1"].category != "urgent" || rows["m1"].attempts != 1 {
		t.Errorf("m1 after a failed action = %+v", rows["m1"])
	}

	// The model would now answer differently; the first answer is kept.
	calls := e.model.count()
	e.model.setReply(func(string, string) (string, error) { return "low", nil })
	e.tools.set("mail_move", func(map[string]any) (string, error) { return "moved", nil })
	rec = e.run("inbox-priority")
	if rec.Acted != 3 || rec.Failed != 0 {
		t.Fatalf("replay run = %+v", rec)
	}
	if e.model.count() != calls {
		t.Errorf("the replay called the model %d more times", e.model.count()-calls)
	}
	if got := e.moves(); got["m1"] != "Urgent" || got["m3"] != "Urgent" {
		t.Errorf("replayed moves = %v, want the first category", got)
	}
	if got := e.processed("inbox-priority")["m1"].status; got != StatusActed {
		t.Errorf("m1 status = %s", got)
	}
}

// A classified row whose action is still pending (the process stopped
// between the two writes) is acted on without a model call.
func TestRunActsOnPendingClassification(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	st, err := e.svc.state(context.Background(), e.scope.BaseDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.setClassified(context.Background(), "inbox-priority", "m2", "urgent", "earlier", "cheap", testStart); err != nil {
		t.Fatal(err)
	}
	e.run("inbox-priority")
	if got := e.moves()["m2"]; got != "Urgent" {
		t.Errorf("m2 moved to %v, want Urgent (the stored category)", got)
	}
	if e.model.count() != 2 {
		t.Errorf("model calls = %d, want 2 (m2 reused)", e.model.count())
	}
}

// Dry-run decisions are kept apart: no action runs, the live state is not
// touched, and switching to live classifies everything again (design 7.1e).
func TestRunDryRunKeptSeparate(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["dry_run"] = true
	e.create(def)
	rec := e.run("inbox-priority")
	if !rec.DryRun || rec.Classified != 3 || rec.Acted != 0 || len(e.tools.callsOf("mail_move")) != 0 {
		t.Fatalf("dry run = %+v, moves %d", rec, len(e.tools.callsOf("mail_move")))
	}
	if rows := e.processed("inbox-priority"); len(rows) != 0 {
		t.Errorf("a dry run wrote the live state: %v", rows)
	}
	entries := e.activity(ActivityQuery{})
	for _, en := range entries {
		if !en.DryRun || en.Outcome != OutcomeRecorded || en.Action != "mail_move" {
			t.Errorf("dry-run activity = %+v", en)
		}
	}
	// A second dry run skips the decided items.
	calls := e.model.count()
	if rec = e.run("inbox-priority"); rec.NewItems != 0 || e.model.count() != calls {
		t.Errorf("second dry run = %+v", rec)
	}

	if err := e.svc.Update(context.Background(), e.scope, "inbox-priority", []byte(`{"dry_run": false}`)); err != nil {
		t.Fatal(err)
	}
	rec = e.run("inbox-priority")
	if rec.DryRun || rec.NewItems != 3 || rec.Acted != 3 || e.model.count() != calls+3 {
		t.Errorf("live run after the dry run = %+v", rec)
	}
}

// classifier_rerun classifies one item again with the current definition,
// replaces its outcome and runs the action; classifier_test stores nothing.
func TestRerunAndTest(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.run("inbox-priority")

	e.model.setReply(func(string, string) (string, error) { return "low - changed my mind", nil })
	d, err := e.svc.Test(context.Background(), e.scope, "inbox-priority", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Category != "low" || d.Reason != "changed my mind" || d.Action != "mail_move" || d.Outcome != OutcomeRecorded {
		t.Errorf("Test = %+v", d)
	}
	if got := e.processed("inbox-priority")["m1"].category; got != "urgent" {
		t.Errorf("Test changed the stored category to %s", got)
	}
	if n := len(e.tools.callsOf("mail_move")); n != 3 {
		t.Errorf("Test ran an action: %d moves", n)
	}
	// Without an item id, Test takes the first item listed.
	if d, err = e.svc.Test(context.Background(), e.scope, "inbox-priority", ""); err != nil || d.ItemID != "m1" {
		t.Errorf("Test(first) = %+v, %v", d, err)
	}

	d, err = e.svc.Rerun(context.Background(), e.scope, "inbox-priority", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Category != "low" || !d.Rerun || d.Outcome != OutcomeActed {
		t.Errorf("Rerun = %+v", d)
	}
	moves := e.tools.callsOf("mail_move")
	if last := moves[len(moves)-1]; last.args["id"] != "m1" || last.args["destination"] != "low" {
		t.Errorf("rerun action = %v", last.args)
	}
	if got := e.processed("inbox-priority")["m1"]; got.category != "low" || got.status != StatusActed {
		t.Errorf("m1 after rerun = %+v", got)
	}
	entries := e.activity(ActivityQuery{Limit: 1})
	if len(entries) != 1 || !entries[0].Rerun || entries[0].ItemID != "m1" {
		t.Errorf("newest activity = %+v", entries)
	}
	if _, err := e.svc.Rerun(context.Background(), e.scope, "inbox-priority", "nope"); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("Rerun(unknown item) = %v", err)
	}
}

// A reply that is not a category is retried once with the error stated; a
// second failure makes the item unclassified, which is not retried and runs
// no action (design 5.1c, 6.1b).
func TestRunRetriesOnceThenUnclassified(t *testing.T) {
	e := newEnv(t)
	e.setInbox([]string{"m1"})
	e.create(definition())
	var mu sync.Mutex
	n := 0
	e.model.setReply(func(_, user string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return "probably important", nil
		}
		if !strings.Contains(user, `Your previous reply "probably important" is not one of the categories. Reply with one of: urgent, low.`) {
			t.Errorf("retry does not state the error:\n%s", user)
		}
		return "Urgent.", nil
	})
	if rec := e.run("inbox-priority"); rec.Acted != 1 || e.model.count() != 2 {
		t.Fatalf("retry run = %+v, model calls %d", rec, e.model.count())
	}

	e2 := newEnv(t)
	e2.setInbox([]string{"m1"})
	e2.create(definition())
	e2.model.setReply(func(string, string) (string, error) { return "no idea", nil })
	rec := e2.run("inbox-priority")
	if rec.Unclassified != 1 || rec.Acted != 0 || e2.model.count() != 2 || len(e2.tools.callsOf("mail_move")) != 0 {
		t.Fatalf("unclassified run = %+v, model calls %d", rec, e2.model.count())
	}
	if got := e2.processed("inbox-priority")["m1"].status; got != StatusUnclassified {
		t.Errorf("status = %s", got)
	}
	if rec = e2.run("inbox-priority"); rec.NewItems != 0 || e2.model.count() != 2 {
		t.Errorf("an unclassified item was retried: %+v", rec)
	}
	if got := e2.notifier.all(); len(got) != 1 || !strings.Contains(got[0], "Classifier inbox-priority: 1 new items, 0 acted, 1 unclassified, 0 failed") {
		t.Errorf("summaries = %v", got)
	}
}

// Processed ids are kept processed_days, dry-run decisions too, and
// activity activity_hours; older rows are pruned at the start of a run.
func TestRunPrunes(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["activity_hours"] = 2
	e.create(def)
	e.run("inbox-priority")

	e.clock.Advance(3 * time.Hour)
	e.setInbox([]string{"m1"}) // m2 and m3 leave the list
	e.run("inbox-priority")
	if entries := e.activity(ActivityQuery{}); len(entries) != 0 {
		t.Errorf("activity older than activity_hours kept: %d entries", len(entries))
	}
	if len(e.processed("inbox-priority")) != 3 {
		t.Error("processed ids pruned before processed_days")
	}

	// m1 is still listed after processed_days: kept, never reprocessed. m2
	// and m3 left the list more than processed_days ago: pruned.
	e.clock.Advance(31 * 24 * time.Hour)
	calls, moves := e.model.count(), len(e.tools.callsOf("mail_move"))
	rec := e.run("inbox-priority")
	if rec.NewItems != 0 || e.model.count() != calls || len(e.tools.callsOf("mail_move")) != moves {
		t.Errorf("an item still listed was reprocessed: %+v", rec)
	}
	rows := e.processed("inbox-priority")
	if len(rows) != 1 || rows["m1"].status != StatusActed {
		t.Errorf("processed rows after pruning = %v, want m1 only", rows)
	}

	// An item that returns after it was pruned is new again.
	e.setInbox([]string{"m1", "m2"})
	if rec = e.run("inbox-priority"); rec.NewItems != 1 || e.model.count() != calls+1 {
		t.Errorf("a returning pruned item = %+v", rec)
	}
}

// Dry-run decisions follow the same rule: kept while listed, pruned after
// leaving the list for processed_days.
func TestRunPrunesDryRun(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["dry_run"] = true
	e.create(def)
	e.run("inbox-priority")
	e.setInbox([]string{"m1"})
	e.clock.Advance(31 * 24 * time.Hour)
	calls := e.model.count()
	if rec := e.run("inbox-priority"); rec.NewItems != 0 || e.model.count() != calls {
		t.Errorf("a listed dry-run item was decided again: %+v", rec)
	}
	st, err := e.svc.state(context.Background(), e.scope.BaseDirectory)
	if err != nil {
		t.Fatal(err)
	}
	decided, err := st.dryDecided(context.Background(), "inbox-priority")
	if err != nil {
		t.Fatal(err)
	}
	if len(decided) != 1 || !decided["m1"] {
		t.Errorf("dry-run decisions after pruning = %v, want m1 only", decided)
	}
}

// A list naming an item twice handles it once, live and in a dry run.
func TestRunDeduplicatesIDs(t *testing.T) {
	for _, dry := range []bool{false, true} {
		e := newEnv(t)
		e.setInbox([]string{"m1", "m1", "m2"})
		def := definition()
		def["dry_run"] = dry
		e.create(def)
		if rec := e.run("inbox-priority"); rec.NewItems != 2 || e.model.count() != 2 {
			t.Errorf("dry_run=%v: run = %+v, model calls %d", dry, rec, e.model.count())
		}
	}
}

// limits.parallel items are in flight at once.
func TestRunParallel(t *testing.T) {
	e := newEnv(t)
	ids := []string{"a1", "a2", "a3", "a4", "a5", "a6"}
	e.setInbox(ids)
	def := definition()
	def["limits"] = map[string]any{"parallel": 3}
	e.create(def)

	var mu sync.Mutex
	inFlight, peak := 0, 0
	release := make(chan struct{})
	reached := make(chan struct{})
	var reachedOnce sync.Once
	e.model.setReply(func(string, string) (string, error) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		if inFlight == 3 {
			// Three in flight again after the release is fine; report once.
			reachedOnce.Do(func() { close(reached) })
		}
		mu.Unlock()
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		return "low", nil
	})
	done := make(chan RunRecord)
	go func() { done <- e.run("inbox-priority") }()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("three items were never in flight together")
	}
	close(release)
	rec := <-done
	if rec.Acted != len(ids) {
		t.Errorf("run = %+v", rec)
	}
	if peak != 3 {
		t.Errorf("peak in flight = %d, want 3", peak)
	}
}

// After stop_after_failures consecutive failures the run starts no more
// items; they are taken next run (design 6.1d).
func TestRunStopsAfterFailures(t *testing.T) {
	e := newEnv(t)
	e.setInbox([]string{"a1", "a2", "a3", "a4", "a5"})
	def := definition()
	def["limits"] = map[string]any{"stop_after_failures": 2}
	e.create(def)
	e.tools.set("mail_move", func(map[string]any) (string, error) { return "", errBoom })
	rec := e.run("inbox-priority")
	if rec.Status != RunStopped || rec.Failed != 2 || e.model.count() != 2 || !strings.Contains(rec.Error, "2 consecutive failures") {
		t.Fatalf("run = %+v, model calls %d", rec, e.model.count())
	}
	// A success in between resets the count.
	e2 := newEnv(t)
	e2.setInbox([]string{"a1", "a2", "a3", "a4"})
	e2.create(def)
	var mu sync.Mutex
	n := 0
	e2.tools.set("mail_move", func(map[string]any) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n%2 == 0 {
			return "moved", nil
		}
		return "", errBoom
	})
	if rec := e2.run("inbox-priority"); rec.Status != RunOK || rec.Failed != 2 || rec.Acted != 2 {
		t.Errorf("alternating run = %+v", rec)
	}
}

// items_per_run caps a run; the rest are taken next run, in source order.
func TestRunItemsPerRun(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["limits"] = map[string]any{"items_per_run": 2}
	e.create(def)
	if rec := e.run("inbox-priority"); rec.NewItems != 2 || rec.Listed != 3 {
		t.Fatalf("first run = %+v", rec)
	}
	if _, ok := e.moves()["m3"]; ok {
		t.Error("m3 handled before m1 and m2's run ended")
	}
	if rec := e.run("inbox-priority"); rec.NewItems != 1 || e.moves()["m3"] != "Urgent" {
		t.Errorf("second run = %+v, moves %v", rec, e.moves())
	}
}

// A placeholder that resolves to nothing fails that item; nothing empty
// reaches the tool (design 4.3b).
func TestRunSubstitutionError(t *testing.T) {
	e := newEnv(t)
	e.setInbox([]string{"m1"})
	def := definition()
	obj(obj(def["fetch"])["fields"])["cc"] = "cc.address"
	obj(def["actions"])["urgent"] = map[string]any{"tool": "mail_move", "args": map[string]any{"id": "{{item.id}}", "destination": "{{fetched.cc}}"}}
	e.create(def)
	rec := e.run("inbox-priority")
	if rec.Failed != 1 || len(e.tools.callsOf("mail_move")) != 0 {
		t.Fatalf("run = %+v, moves %d", rec, len(e.tools.callsOf("mail_move")))
	}
	entries := e.activity(ActivityQuery{})
	if len(entries) != 1 || !strings.Contains(entries[0].Error, "{{fetched.cc}} resolves to nothing") {
		t.Errorf("activity = %+v", entries)
	}
	if !strings.Contains(e.model.calls[0], "fields not present: \"cc\"") {
		t.Errorf("the model was not told of the missing field:\n%s", e.model.calls[0])
	}
}

// A model in cooldown ends the run at once, naming the model; the item is
// failed and taken again next run (design 3.2f).
func TestRunStopsOnCooldown(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.model.setReply(func(string, string) (string, error) {
		return "", &CooldownError{Model: "cheap", Remaining: 150 * time.Second}
	})
	rec := e.run("inbox-priority")
	if rec.Status != RunError || rec.Failed != 1 || e.model.count() != 1 || rec.Error != "model cheap is in cooldown for 2m30s" {
		t.Fatalf("run = %+v, model calls %d", rec, e.model.count())
	}
	if got := e.notifier.all(); len(got) != 1 || !strings.Contains(got[0], "1 failed (model cheap is in cooldown for 2m30s)") {
		t.Errorf("summary = %v", got)
	}
	e.model.setReply(func(string, string) (string, error) { return "", fmt.Errorf("x: %w", ErrNoModel) })
	if rec = e.run("inbox-priority"); rec.Status != RunError || !strings.Contains(rec.Error, "no usable classifier model") {
		t.Errorf("no-model run = %+v", rec)
	}
}

// A tool the agent no longer has fails the run with the tool named.
func TestRunFailsOnMissingTool(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.tools.remove("mail_move")
	rec := e.run("inbox-priority")
	if rec.Status != RunError || !strings.Contains(rec.Error, "tool mail_move is not available to agent alice") || e.model.count() != 0 {
		t.Errorf("run = %+v", rec)
	}
}

// An element whose source.id resolves to nothing fails the run, naming the
// element's index.
func TestRunFailsOnElementWithoutID(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.tools.set("mail_list", func(map[string]any) (string, error) {
		return `{"messages":[{"id":"m1"},{"subject":"no id"}]}`, nil
	})
	rec := e.run("inbox-priority")
	if rec.Status != RunError || !strings.Contains(rec.Error, `source.id "id" resolves to nothing in element 1`) || e.model.count() != 0 {
		t.Errorf("run = %+v", rec)
	}
}

// report.summary: on_problems (default) posts only when items failed or were
// unclassified; always after a run that handled items; never not at all.
func TestRunSummaryModes(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		failing  bool
		wantSent bool
	}{
		{"", false, false},
		{"", true, true},
		{"always", false, true},
		{"never", true, false},
	} {
		t.Run(tc.mode+strconv.FormatBool(tc.failing), func(t *testing.T) {
			e := newEnv(t)
			def := definition()
			if tc.mode != "" {
				def["report"] = map[string]any{"summary": tc.mode}
			}
			e.create(def)
			if tc.failing {
				e.tools.set("mail_move", func(map[string]any) (string, error) { return "", errBoom })
			}
			e.run("inbox-priority")
			if sent := len(e.notifier.all()) == 1; sent != tc.wantSent {
				t.Errorf("summary sent = %v, want %v (%v)", sent, tc.wantSent, e.notifier.all())
			}
			// A run with nothing new posts nothing, even with always.
			before := len(e.notifier.all())
			if !tc.failing {
				e.run("inbox-priority")
				if len(e.notifier.all()) != before {
					t.Errorf("a run with nothing new posted a summary")
				}
			}
		})
	}
}

// Without fetch, the model sees the list element's members.
func TestRunWithoutFetch(t *testing.T) {
	e := newEnv(t)
	def := definition()
	delete(def, "fetch")
	def["actions"] = map[string]any{"low": map[string]any{"tool": "mail_move", "args": map[string]any{"id": "{{item.id}}", "destination": "Low"}}}
	e.create(def)
	rec := e.run("inbox-priority")
	if rec.Classified != 3 || len(e.tools.callsOf("mail_read")) != 0 {
		t.Fatalf("run = %+v", rec)
	}
	if !strings.Contains(e.model.calls[0], "field \"subject\"\nAbout m1") {
		t.Errorf("element fields not sent:\n%s", e.model.calls[0])
	}
	// urgent has no action: recorded only, and done.
	if got := e.processed("inbox-priority")["m2"].status; got != StatusActed {
		t.Errorf("recorded-only status = %s", got)
	}
}

// The fields are cut to fetch.max_chars together, longest first, and the
// model is told.
func TestRunTruncatesFields(t *testing.T) {
	e := newEnv(t)
	e.setInbox([]string{"m1"})
	e.tools.set("mail_read", func(map[string]any) (string, error) {
		return fmt.Sprintf(`{"from":{"address":"bob@example.com"},"subject":"short","body":{"content":%q}}`, strings.Repeat("x", 500)), nil
	})
	def := definition()
	obj(def["fetch"])["max_chars"] = 100
	e.create(def)
	e.run("inbox-priority")
	user := e.model.calls[0]
	if !strings.Contains(user, "field \"body\" (shortened)\n"+strings.Repeat("x", 100-len("bob@example.com")-len("short"))+"\n") {
		t.Errorf("body not cut to the budget:\n%s", user)
	}
	if strings.Contains(user, "field \"subject\" (shortened)") || strings.Contains(user, "field \"from\" (shortened)") {
		t.Errorf("a short field was cut:\n%s", user)
	}
}

// Activity search matches fields, item id, category and reason, ignoring
// case; limit and offset page newest first.
func TestActivitySearch(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.run("inbox-priority")
	got, err := e.svc.Activity(e.scope, "inbox-priority", ActivityQuery{Search: "BOB@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("search by sender = %d entries, want 2", len(got))
	}
	if got = e.activity(ActivityQuery{Search: "newsletter"}); len(got) != 1 || got[0].ItemID != "m2" {
		t.Errorf("search by reason = %+v", got)
	}
	if got = e.activity(ActivityQuery{Search: "100%"}); len(got) != 0 {
		t.Errorf("a %% in the search matched %d entries", len(got))
	}
	all := e.activity(ActivityQuery{})
	page := e.activity(ActivityQuery{Limit: 1, Offset: 1})
	if len(page) != 1 || page[0].ItemID != all[1].ItemID {
		t.Errorf("page = %+v, all = %+v", page, all)
	}
}
