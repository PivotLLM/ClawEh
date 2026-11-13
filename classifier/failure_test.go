// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// failingSource makes the source tool fail with msg.
func (e *env) failingSource(msg string) {
	e.tools.set("mail_list", func(map[string]any) (string, error) { return "", errors.New(msg) })
}

// restart replaces the service with a new one over the same folders, as a
// restart of ClawEh would.
func (e *env) restart() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.svc.Close(ctx); err != nil {
		e.t.Fatalf("Close: %v", err)
	}
	e.svc = New(e.svc.host)
}

// update applies patch to inbox-priority.
func (e *env) update(patch string) {
	e.t.Helper()
	if err := e.svc.Update(context.Background(), e.scope, "inbox-priority", []byte(patch)); err != nil {
		e.t.Fatalf("Update %s: %v", patch, err)
	}
}

// status is inbox-priority's status.
func (e *env) status() Status {
	e.t.Helper()
	st, err := e.svc.Status(e.scope, "inbox-priority")
	if err != nil || len(st) != 1 {
		e.t.Fatalf("Status = %+v, %v", st, err)
	}
	return st[0]
}

// runFailed runs inbox-priority and fails the test unless the run failed.
func (e *env) runFailed() {
	e.t.Helper()
	if rec := e.run("inbox-priority"); rec.Status != RunError {
		e.t.Fatalf("run = %+v, want a failed run", rec)
	}
}

// runFailures is the run_failed rows of the activity log, newest first.
func (e *env) runFailures() []ActivityEntry {
	return e.activity(ActivityQuery{Outcome: OutcomeRunFailed})
}

const (
	wantFailureSummary = "Classifier inbox-priority: the run failed (source mail_list: the tool failed)."
	wantFailureAdvice  = wantFailureSummary + " This looks like a problem with mail_list itself (credentials or an outage); check it before changing the classifier."
	wantFailureDetail  = "\n<<untrusted>>the mailbox is unreachable<</untrusted>>"
	wantFailureNotice  = wantFailureAdvice + wantFailureDetail
	wantMuteNotice     = wantFailureAdvice + " Further errors from this classifier won't be reported for 24 hours. " +
		`To report them again now, call classifier_update with {"report": {"errors": true}}.` + wantFailureDetail
	wantMuteSummary = wantFailureSummary + " Further errors won't be reported for 24 hours."
)

// notices returns the texts of the agent's notices and of the chat
// summaries, in order.
func (e *env) notices() (agent, chat []string) {
	for _, n := range e.notifier.allNotices() {
		agent = append(agent, n.text)
	}
	return agent, e.notifier.all()
}

// expectNotices checks the notices and summaries sent so far.
func (e *env) expectNotices(step string, wantAgent, wantChat []string) {
	e.t.Helper()
	agent, chat := e.notices()
	if strings.Join(agent, "\n") != strings.Join(wantAgent, "\n") {
		e.t.Errorf("%s: agent notices = %q, want %q", step, agent, wantAgent)
	}
	if strings.Join(chat, "\n") != strings.Join(wantChat, "\n") {
		e.t.Errorf("%s: chat summaries = %q, want %q", step, chat, wantChat)
	}
}

// The first failed run sends a notice to the agent and, under on_problems
// and always, the chat; the second also says further errors are muted for
// 24 hours; later ones are only logged until the mute has expired, when
// the next failure is the first notice of a new cycle. Under never the
// chat gets nothing, the agent the same notices.
func TestRunFailureNotices(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		wantChat bool
	}{
		{"", true},
		{SummaryOnProblems, true},
		{SummaryAlways, true},
		{SummaryNever, false},
	} {
		t.Run("mode "+tc.mode, func(t *testing.T) {
			e := newEnv(t)
			def := definition()
			if tc.mode != "" {
				def["report"] = map[string]any{"summary": tc.mode}
			}
			e.create(def)
			e.failingSource("the mailbox is unreachable")
			chat := func(texts ...string) []string {
				if !tc.wantChat {
					return nil
				}
				return texts
			}
			first := []string{wantFailureNotice}
			second := []string{wantFailureNotice, wantMuteNotice}

			e.runFailed()
			e.expectNotices("first failure", first, chat(wantFailureSummary))
			if n := e.notifier.allNotices(); n[0].agent != "alice" || n[0].classifier != "inbox-priority" {
				t.Errorf("notice = %+v", n[0])
			}
			e.clock.Advance(time.Minute)
			e.runFailed()
			e.expectNotices("second failure", second, chat(wantFailureSummary, wantMuteSummary))
			muteEnds := e.clock.Now().Add(ErrorMute)
			if st := e.status(); st.FailedRuns != 2 || !st.ErrorsMutedUntil.Equal(muteEnds) {
				t.Errorf("status after the second failure: failed_runs %d, muted until %v; want 2, %v", st.FailedRuns, st.ErrorsMutedUntil, muteEnds)
			}
			for range 3 {
				e.clock.Advance(time.Hour)
				e.runFailed()
			}
			e.expectNotices("muted", second, chat(wantFailureSummary, wantMuteSummary))
			if rows := e.runFailures(); len(rows) != 1 || rows[0].Count != 5 {
				t.Errorf("run_failed rows = %+v, want one with count 5", rows)
			}
			if st := e.status(); st.FailedRuns != 5 {
				t.Errorf("failed_runs = %d, want 5", st.FailedRuns)
			}

			e.clock.Set(muteEnds)
			e.runFailed()
			e.expectNotices("mute expired", append(second, wantFailureNotice), chat(wantFailureSummary, wantMuteSummary, wantFailureSummary))
			if st := e.status(); st.FailedRuns != 1 || !st.ErrorsMutedUntil.IsZero() {
				t.Errorf("status after the mute: failed_runs %d, muted until %v; want 1, none", st.FailedRuns, st.ErrorsMutedUntil)
			}
			e.runFailed()
			e.expectNotices("second of the new cycle", append(second, second...),
				chat(wantFailureSummary, wantMuteSummary, wantFailureSummary, wantMuteSummary))
		})
	}
}

// A run that does not fail resets the count, not the mute: under a mute
// the next failure is still only logged; without one it is a first notice
// again. The successful run posts nothing.
func TestRunFailureCountResetBySuccess(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.failingSource("the mailbox is unreachable")
	e.runFailed()
	e.setInbox(nil)
	if rec := e.run("inbox-priority"); rec.Status != RunOK {
		t.Fatalf("recovered run = %+v", rec)
	}
	if st := e.status(); st.FailedRuns != 0 {
		t.Errorf("failed_runs after a success = %d", st.FailedRuns)
	}
	e.failingSource("the mailbox is unreachable")
	e.runFailed()
	e.expectNotices("after a success", []string{wantFailureNotice, wantFailureNotice}, []string{wantFailureSummary, wantFailureSummary})

	e.runFailed() // the second in a row: muted
	e.setInbox(nil)
	e.run("inbox-priority")
	e.failingSource("the mailbox is unreachable")
	e.runFailed()
	agent, _ := e.notices()
	if len(agent) != 3 || agent[2] != wantMuteNotice {
		t.Errorf("agent notices = %q, want the mute notice last", agent)
	}
	if st := e.status(); st.FailedRuns != 1 || st.ErrorsMutedUntil.IsZero() {
		t.Errorf("status = failed_runs %d, muted until %v; want 1 and muted", st.FailedRuns, st.ErrorsMutedUntil)
	}
}

// A run stopped by stop_after_failures is a failed run: its error names the
// cause the items share (stable, so runs fold), the last item's error is
// the row's detail and, marked untrusted, ends the agent's notice; it
// counts toward the notices and their mute like any failed run, and its
// advice fits the cause (here a tool's). A persistent item failure so
// reaches the agent.
func TestRunStoppedByFailuresIsAFailedRun(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["limits"] = map[string]any{"stop_after_failures": 2}
	e.create(def)
	e.tools.set("mail_read", func(map[string]any) (string, error) { return "", errors.New("403 Forbidden") })
	const wantErr = "stopped after 2 consecutive item failures: fetch mail_read: the tool failed"
	const wantSummary = "Classifier inbox-priority: the run failed (" + wantErr + ")."
	const wantAdvice = wantSummary + " This looks like a problem with mail_read itself (credentials or an outage); check it before changing the classifier."
	const wantDetail = "\n<<untrusted>>fetch mail_read: the tool failed: 403 Forbidden<</untrusted>>"
	rec := e.run("inbox-priority")
	if rec.Status != RunError || rec.Error != wantErr || rec.Failed != 2 {
		t.Fatalf("run = %+v, want a failed run with %q", rec, wantErr)
	}
	e.expectNotices("first", []string{wantAdvice + wantDetail}, []string{wantSummary})
	if rows := e.runFailures(); len(rows) != 1 || rows[0].Error != wantErr || rows[0].Count != 1 ||
		rows[0].Detail != "fetch mail_read: the tool failed: 403 Forbidden" {
		t.Errorf("run_failed rows = %+v", rows)
	}
	if e.log.warnings("run error: 3 listed, 3 new, 0 classified, 0 acted, 0 unclassified, 2 failed ("+wantErr+")") != 1 {
		t.Errorf("warnings = %q, want the failed run logged at warn", e.log.warns)
	}
	e.runFailed()
	e.expectNotices("second", []string{wantAdvice + wantDetail, wantAdvice + " " + muteNotice + wantDetail},
		[]string{wantSummary, wantSummary + " " + muteSummary})
	if st := e.status(); st.FailedRuns != 2 || st.ErrorsMutedUntil.IsZero() {
		t.Errorf("status = failed_runs %d, muted until %v; want 2 and muted", st.FailedRuns, st.ErrorsMutedUntil)
	}
	if rows := e.runFailures(); len(rows) != 1 || rows[0].Count != 2 {
		t.Errorf("run_failed rows after the second run = %+v, want one folded row", rows)
	}
}

// A run stopped because the classifier was stopped is neither a failure
// nor a success: it leaves the count as it was.
func TestRunFailureCountKeptByStoppedRun(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.failingSource("the mailbox is unreachable")
	e.runFailed()
	e.setInbox(inboxIDs)
	stop := make(chan struct{})
	close(stop)
	if rec := e.svc.runClassifier(context.Background(), e.scope, e.def(), stop); rec.Status != RunStopped {
		t.Fatalf("run = %+v, want stopped", rec)
	}
	if st := e.status(); st.FailedRuns != 1 {
		t.Errorf("failed_runs after a stopped run = %d, want 1", st.FailedRuns)
	}
}

// report.errors false: failed runs are logged, counted and never reported,
// to the agent or the chat.
func TestRunFailureErrorsOff(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["report"] = map[string]any{"summary": SummaryAlways, "errors": false}
	e.create(def)
	e.failingSource("the mailbox is unreachable")
	for range 3 {
		e.runFailed()
	}
	e.expectNotices("errors off", nil, nil)
	if rows := e.runFailures(); len(rows) != 1 || rows[0].Count != 3 {
		t.Errorf("run_failed rows = %+v", rows)
	}
	st := e.status()
	if st.ErrorsReported == nil || *st.ErrorsReported || st.FailedRuns != 3 || !st.ErrorsMutedUntil.IsZero() {
		t.Errorf("status = %+v", st)
	}
}

// Any change of the definition by the agent lifts the mute and resets the
// count: setting report.errors (to true, to the value it has, or to false)
// or any other change. The next failure is a first notice.
func TestRunFailureMuteClearedByUpdate(t *testing.T) {
	for _, patch := range []string{
		`{"report": {"errors": true}}`,
		`{"report": {"errors": false}}`,
		`{"limits": {"parallel": 2}}`,
	} {
		t.Run(patch, func(t *testing.T) {
			e := newEnv(t)
			e.create(definition())
			if patch == `{"report": {"errors": true}}` {
				e.update(patch) // true already: setting it again still clears
			}
			e.failingSource("the mailbox is unreachable")
			e.runFailed()
			e.runFailed()
			e.runFailed() // muted
			e.setInbox(inboxIDs)
			e.update(patch)
			if st := e.status(); st.FailedRuns != 0 || !st.ErrorsMutedUntil.IsZero() {
				t.Errorf("status after the update = failed_runs %d, muted until %v", st.FailedRuns, st.ErrorsMutedUntil)
			}
			e.failingSource("the mailbox is unreachable")
			e.runFailed()
			agent, _ := e.notices()
			want := 3 // first, second, and a first again
			if patch == `{"report": {"errors": false}}` {
				want = 2 // off now: logged only
			}
			if len(agent) != want || (want == 3 && agent[2] != wantFailureNotice) {
				t.Errorf("agent notices = %q, want %d", agent, want)
			}
		})
	}
}

// The count and the mute are in state.db: a restart keeps them.
func TestRunFailureMuteSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.failingSource("the mailbox is unreachable")
	e.runFailed()
	e.runFailed()
	e.restart()
	e.runFailed()
	e.expectNotices("after a restart", []string{wantFailureNotice, wantMuteNotice}, []string{wantFailureSummary, wantMuteSummary})
	if st := e.status(); st.FailedRuns != 3 || st.ErrorsMutedUntil.IsZero() {
		t.Errorf("status = failed_runs %d, muted until %v", st.FailedRuns, st.ErrorsMutedUntil)
	}
}

// Item failures keep their summary and neither count nor get muted.
func TestRunFailureItemFailuresUnaffected(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.failingSource("the mailbox is unreachable")
	e.runFailed()
	e.runFailed() // muted now
	e.setInbox(inboxIDs)
	e.tools.set("mail_move", func(map[string]any) (string, error) { return "", errors.New("folder gone") })
	if rec := e.run("inbox-priority"); rec.Status != RunOK || rec.Failed != 3 {
		t.Fatalf("run = %+v", rec)
	}
	_, chat := e.notices()
	if len(chat) != 3 || !strings.Contains(chat[2], "3 failed") {
		t.Errorf("chat summaries = %q, want the item failures posted", chat)
	}
	if st := e.status(); st.FailedRuns != 0 {
		t.Errorf("failed_runs = %d", st.FailedRuns)
	}
}

// Failed runs are folded into one run_failed row while the error repeats;
// a different error, a run that did not fail, item activity or a dry-run
// change starts a new row. Rows have no item, carry the dry-run flag, and
// are found by the outcome filter and by searching the error text.
func TestRunFailureRows(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.failingSource("the mailbox is unreachable")
	first := e.clock.Now()
	e.runFailed()
	e.clock.Advance(time.Minute)
	e.runFailed()
	last := e.clock.Now()
	rows := e.runFailures()
	if len(rows) != 1 || rows[0].ItemID != "" || rows[0].Count != 2 || !rows[0].At.Equal(first) || !rows[0].LastAt.Equal(last) ||
		rows[0].Error != "source mail_list: the tool failed: the mailbox is unreachable" || rows[0].DryRun || len(rows[0].Fields) != 0 {
		t.Fatalf("rows = %+v", rows)
	}

	e.clock.Advance(time.Minute)
	e.failingSource("the mailbox is gone")
	e.runFailed() // a different error
	e.failingSource("the mailbox is unreachable")
	e.runFailed() // back to the first error: a new row too
	if rows = e.runFailures(); len(rows) != 3 || rows[0].Count != 1 || rows[1].Error != "source mail_list: the tool failed: the mailbox is gone" {
		t.Fatalf("rows after a change = %+v", rows)
	}

	e.setInbox(nil)
	e.run("inbox-priority") // ok: closes the row
	e.failingSource("the mailbox is unreachable")
	e.runFailed()
	if rows = e.runFailures(); len(rows) != 4 || rows[0].Count != 1 {
		t.Fatalf("rows after a success = %+v", rows)
	}

	// Item activity in between.
	e.setInbox([]string{"m1"})
	e.run("inbox-priority")
	e.failingSource("the mailbox is unreachable")
	e.runFailed()
	if rows = e.runFailures(); len(rows) != 5 || rows[0].Count != 1 {
		t.Fatalf("rows after item activity = %+v", rows)
	}

	// A dry run is marked and kept apart.
	e.setInbox(nil)
	e.update(`{"dry_run": true}`)
	e.failingSource("the mailbox is unreachable")
	e.runFailed()
	if rows = e.runFailures(); len(rows) != 6 || !rows[0].DryRun || rows[0].Count != 1 {
		t.Fatalf("rows after dry_run = %+v", rows)
	}

	if got := e.activity(ActivityQuery{Search: "MAILBOX IS GONE"}); len(got) != 1 || got[0].Outcome != OutcomeRunFailed {
		t.Errorf("search on the error = %+v", got)
	}
	if got := e.activity(ActivityQuery{Outcome: OutcomeActed}); len(got) != 1 || got[0].ItemID != "m1" {
		t.Errorf("acted = %+v", got)
	}
	// Status's recent decisions are the items only.
	if st := e.status(); len(st.Recent) != 1 || st.Recent[0].ItemID != "m1" {
		t.Errorf("recent = %+v", st.Recent)
	}
}

// A model cooldown is not a failed run: no run_failed row, no notice, the
// count unchanged.
func TestRunFailureCooldownNotLogged(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	e.model.setReply(func(string, string) (string, error) {
		return "", &CooldownError{Model: "cheap", Remaining: time.Minute}
	})
	if rec := e.run("inbox-priority"); rec.Status != RunError {
		t.Fatalf("run = %+v", rec)
	}
	if rows := e.runFailures(); len(rows) != 0 {
		t.Errorf("run_failed rows = %+v", rows)
	}
	if st := e.status(); st.FailedRuns != 0 || len(e.notifier.allNotices()) != 0 {
		t.Errorf("failed_runs %d, notices %+v", st.FailedRuns, e.notifier.allNotices())
	}
}

// A run_failed row is kept while the same error goes on, and pruned
// activity_hours after its last failure.
func TestRunFailureRowRetention(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["activity_hours"] = 2
	e.create(def)
	e.failingSource("the mailbox is unreachable")
	for range 4 { // over 3 hours, more than activity_hours since the first
		e.runFailed()
		e.clock.Advance(time.Hour)
	}
	if rows := e.runFailures(); len(rows) != 1 || rows[0].Count != 4 {
		t.Fatalf("rows = %+v", rows)
	}
	e.clock.Advance(2 * time.Hour)
	e.setInbox(nil)
	e.run("inbox-priority") // prunes
	if rows := e.runFailures(); len(rows) != 0 {
		t.Errorf("rows after activity_hours = %+v", rows)
	}
}

// An outcome that does not exist is refused, naming the valid ones.
func TestActivityOutcomeRefused(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	_, err := e.svc.Activity(e.scope, "inbox-priority", ActivityQuery{Outcome: "classified"})
	if err == nil || err.Error() != "outcome must be acted, recorded, unclassified, failed, run_failed." {
		t.Errorf("err = %v", err)
	}
}

// A classifier a clone created is its source agent's: the source agent gets
// the notice.
func TestRunFailureNoticeGoesToCloneSource(t *testing.T) {
	e := newEnv(t)
	clone := e.scope
	clone.Clone = "1a2b3c4d"
	if _, err := e.svc.Create(context.Background(), clone, mustJSON(t, definition())); err != nil {
		t.Fatalf("Create: %v", err)
	}
	e.failingSource("the mailbox is unreachable")
	e.run("inbox-priority")
	if notices := e.notifier.allNotices(); len(notices) != 1 || notices[0].agent != "alice" {
		t.Errorf("notices = %+v, want one to alice", notices)
	}
}

// A failed run of a dry-run classifier says so.
func TestFailureTextDryRun(t *testing.T) {
	def := &Definition{ID: "inbox"}
	got := failureText(def, RunRecord{DryRun: true, Error: "classify.instructions_dir files/rules can't be read: it does not exist"})
	want := "Classifier inbox (dry run): the run failed (classify.instructions_dir files/rules can't be read: it does not exist)."
	if got != want {
		t.Errorf("failureText = %q, want %q", got, want)
	}
}

// A state.db written before run_failed rows existed (activity.item_id NOT
// NULL, no count or last_at, no run_failures) is migrated on open, keeping
// its rows; a failed run is then logged without an item and its notice
// counted.
func TestOpenStateMigratesRunFailures(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	db, err := sql.Open("sqlite", stateDSN(filepath.Join(base, "state.db"), false))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE activity (
  id INTEGER PRIMARY KEY, classifier TEXT NOT NULL, at INTEGER NOT NULL, item_id TEXT NOT NULL,
  fields TEXT NOT NULL,
  category TEXT, reason TEXT, model TEXT, action TEXT, outcome TEXT NOT NULL, error TEXT,
  dry_run INTEGER NOT NULL, rerun INTEGER NOT NULL, action_args TEXT)`,
		`CREATE INDEX activity_by_time ON activity (classifier, at DESC)`,
		`INSERT INTO activity (classifier, at, item_id, fields, outcome, dry_run, rerun, action_args) VALUES ('c', 1, 'old', '{"subject":"Hi"}', 'acted', 0, 0, '{"id":"old"}')`,
		`CREATE TABLE runs (
  classifier TEXT PRIMARY KEY, started_at INTEGER, ended_at INTEGER, status TEXT, error TEXT,
  listed INTEGER, new_items INTEGER, classified INTEGER, acted INTEGER, unclassified INTEGER, failed INTEGER, dry_run INTEGER)`,
		`INSERT INTO runs VALUES ('c', 1, 2, 'error', 'boom', 0, 0, 0, 0, 0, 0, 0)`,
	} {
		if _, err = db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	reopen(t, base) // migrates
	reopen(t, base) // finds it done
	st, err := openState(ctx, base, 0o700, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeOrFail(t, st) })
	now := time.UnixMilli(5).UTC()
	if err = st.addRunFailure(ctx, "c", "boom", "", false, now); err != nil {
		t.Fatal(err)
	}
	if err = st.addRunFailure(ctx, "c", "boom", "", false, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := st.activity(ctx, "c", ActivityQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Outcome != OutcomeRunFailed || got[0].ItemID != "" || got[0].Count != 2 ||
		!got[0].LastAt.Equal(now.Add(time.Second)) || got[1].ItemID != "old" || got[1].Fields["subject"] != "Hi" ||
		got[1].ActionArgs != `{"id":"old"}` || got[1].Count != 0 || !got[1].LastAt.IsZero() {
		t.Errorf("activity = %+v", got)
	}
	if f, fErr := runFailuresFrom(ctx, st.db, "c"); fErr != nil || f.failures != 0 {
		t.Errorf("run failures = %+v, %v", f, fErr)
	}
	var index int
	if err = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'activity_by_time' AND tbl_name = 'activity'`).Scan(&index); err != nil || index != 1 {
		t.Errorf("activity_by_time index: %d, %v", index, err)
	}
	r, err := lastRunFrom(ctx, st.db, "c")
	if err != nil || r == nil || r.Error != "boom" {
		t.Errorf("last run = %+v, %v", r, err)
	}
}
