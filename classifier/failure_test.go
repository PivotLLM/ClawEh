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

const (
	wantFailureSummary = "Classifier inbox-priority: the run failed: source mail_list: the mailbox is unreachable."
	wantFailureNotice  = wantFailureSummary + " Check and fix the classifier's definition or its instructions files; classifier_test shows the result."
)

// A failed run is posted to the chat under on_problems and always, not
// under never, and is delivered to the owning agent in every mode, naming
// the classifier and the error.
func TestRunFailureReported(t *testing.T) {
	for _, tc := range []struct {
		mode        string
		wantSummary bool
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
			if rec := e.run("inbox-priority"); rec.Status != RunError {
				t.Fatalf("run = %+v", rec)
			}
			sums := e.notifier.all()
			if tc.wantSummary && (len(sums) != 1 || sums[0] != wantFailureSummary) {
				t.Errorf("summaries = %q, want [%q]", sums, wantFailureSummary)
			}
			if !tc.wantSummary && len(sums) != 0 {
				t.Errorf("summaries = %q, want none", sums)
			}
			notices := e.notifier.allNotices()
			want := notice{agent: "alice", classifier: "inbox-priority", text: wantFailureNotice}
			if len(notices) != 1 || notices[0] != want {
				t.Errorf("notices = %+v, want [%+v]", notices, want)
			}
		})
	}
}

// A failure is reported once per distinct error: not again while the same
// error repeats (also across a restart), again when it changes, and again
// after a run that did not fail. The successful run posts nothing.
func TestRunFailureReportedOnce(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	counts := func(step string, wantSummaries, wantNotices int) {
		t.Helper()
		if got := len(e.notifier.all()); got != wantSummaries {
			t.Errorf("%s: %d summaries, want %d: %q", step, got, wantSummaries, e.notifier.all())
		}
		if got := len(e.notifier.allNotices()); got != wantNotices {
			t.Errorf("%s: %d notices, want %d: %+v", step, got, wantNotices, e.notifier.allNotices())
		}
	}

	e.failingSource("the mailbox is unreachable")
	e.run("inbox-priority")
	counts("first failure", 1, 1)
	e.run("inbox-priority")
	e.run("inbox-priority")
	counts("same error again", 1, 1)

	e.restart()
	e.run("inbox-priority")
	counts("same error after a restart", 1, 1)

	e.failingSource("the mailbox is gone")
	e.run("inbox-priority")
	counts("changed error", 2, 2)
	if got := e.notifier.allNotices()[1].text; !strings.HasPrefix(got, "Classifier inbox-priority: the run failed: source mail_list: the mailbox is gone. Check") {
		t.Errorf("second notice = %q", got)
	}

	// Recovered: nothing new to classify, nothing posted.
	e.setInbox(nil)
	if rec := e.run("inbox-priority"); rec.Status != RunOK {
		t.Fatalf("recovered run = %+v", rec)
	}
	counts("recovered", 2, 2)

	e.failingSource("the mailbox is gone")
	e.run("inbox-priority")
	counts("same error after a success", 3, 3)
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
	got := failureText(def, RunRecord{DryRun: true, Error: "classifier inbox: classify.instructions_dir files/rules can't be read: it does not exist"})
	want := "Classifier inbox (dry run): the run failed: classify.instructions_dir files/rules can't be read: it does not exist."
	if got != want {
		t.Errorf("failureText = %q, want %q", got, want)
	}
}

// A state.db written before runs.reported_error existed gets the column on
// open: its failed run counts as not reported yet, so it is reported once.
func TestOpenStateAddsReportedError(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	db, err := sql.Open("sqlite", stateDSN(filepath.Join(base, "state.db"), false))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
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

	reopen(t, base) // adds the column
	st, err := openState(ctx, base, 0o700, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeOrFail(t, st) })
	got, err := st.reportedError(ctx, "c")
	if err != nil || got != "" {
		t.Fatalf("reported error = %q, %v; want none", got, err)
	}
	if err = st.setReportedError(ctx, "c", "boom"); err != nil {
		t.Fatal(err)
	}
	if got, err = st.reportedError(ctx, "c"); err != nil || got != "boom" {
		t.Errorf("reported error = %q, %v; want boom", got, err)
	}
	r, err := lastRunFrom(ctx, st.db, "c")
	if err != nil || r == nil || r.Error != "boom" {
		t.Errorf("last run = %+v, %v", r, err)
	}
}
