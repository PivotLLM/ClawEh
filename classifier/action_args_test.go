// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A dry run records the arguments the action would get, after
// substitution, as compact JSON cut to ActivityValueMax characters; the
// activity search matches them.
func TestRunDryRunRecordsActionArgs(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["dry_run"] = true
	long := strings.Repeat("x", 2*ActivityValueMax)
	obj(def["actions"])["urgent"] = map[string]any{"tool": "mail_move", "args": map[string]any{"id": "{{item.id}}", "destination": "Urgent", "note": long}}
	e.create(def)
	e.run("inbox-priority")
	if n := len(e.tools.callsOf("mail_move")); n != 0 {
		t.Fatalf("a dry run ran %d actions", n)
	}
	args := map[string]string{}
	for _, en := range e.activity(ActivityQuery{}) {
		args[en.ItemID] = en.ActionArgs
	}
	if got := args["m2"]; got != `{"destination":"low","id":"m2"}` {
		t.Errorf("m2 action_args = %s", got)
	}
	urgent := args["m1"]
	if want := `{"destination":"Urgent","id":"m1","note":"xxx`; !strings.HasPrefix(urgent, want) || len([]rune(urgent)) != ActivityValueMax {
		t.Errorf("m1 action_args = %q (%d characters), want %q... cut to %d", urgent, len([]rune(urgent)), want, ActivityValueMax)
	}
	if got := e.activity(ActivityQuery{Search: `"destination":"LOW"`}); len(got) != 1 || got[0].ItemID != "m2" {
		t.Errorf("search by action arguments = %+v", got)
	}
}

// A dry run whose substitution fails fails the item as a live run would,
// runs nothing and records no dry-run decision, so the next dry run tries it
// again.
func TestRunDryRunSubstitutionError(t *testing.T) {
	e := newEnv(t)
	e.setInbox([]string{"m1"})
	def := definition()
	def["dry_run"] = true
	obj(obj(def["fetch"])["fields"])["cc"] = "cc.address"
	obj(def["actions"])["urgent"] = map[string]any{"tool": "mail_move", "args": map[string]any{"id": "{{item.id}}", "destination": "{{fetched.cc}}"}}
	e.create(def)
	rec := e.run("inbox-priority")
	if rec.Failed != 1 || len(e.tools.callsOf("mail_move")) != 0 {
		t.Fatalf("run = %+v, moves %d", rec, len(e.tools.callsOf("mail_move")))
	}
	entries := e.activity(ActivityQuery{})
	if len(entries) != 1 || !entries[0].DryRun || entries[0].Outcome != OutcomeFailed || entries[0].ActionArgs != "" ||
		entries[0].Action != "mail_move" || !strings.Contains(entries[0].Error, "{{fetched.cc}} resolves to nothing") {
		t.Errorf("activity = %+v", entries)
	}
	st, err := e.svc.state(context.Background(), e.scope.BaseDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if decided, err := st.dryDecided(context.Background(), "inbox-priority"); err != nil || len(decided) != 0 {
		t.Errorf("dry-run decisions = %v, %v; want none", decided, err)
	}
}

// A state.db written before activity.action_args existed gets the column on
// open, keeps its rows and records new ones.
func TestOpenStateAddsActionArgs(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	writeOldStateDB(t, filepath.Join(base, "state.db"))

	reopen(t, base) // adds the column
	reopen(t, base) // finds it already there
	st, err := openState(ctx, base, 0o700, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeOrFail(t, st) })
	var entry ActivityEntry
	entry.ItemID, entry.Outcome, entry.DryRun, entry.ActionArgs, entry.At = "new", OutcomeRecorded, true, `{"id":"new"}`, time.UnixMilli(2)
	if err = st.addActivity(ctx, "c", entry); err != nil {
		t.Fatal(err)
	}
	got, err := st.activity(ctx, "c", ActivityQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ItemID != "new" || got[0].ActionArgs != `{"id":"new"}` || got[1].ItemID != "old" || got[1].ActionArgs != "" {
		t.Errorf("activity = %+v", got)
	}
}

// writeOldStateDB writes a state.db whose activity table predates
// action_args, with one row.
func writeOldStateDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", stateDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE activity (
  id INTEGER PRIMARY KEY, classifier TEXT NOT NULL, at INTEGER NOT NULL, item_id TEXT NOT NULL,
  fields TEXT NOT NULL,
  category TEXT, reason TEXT, model TEXT, action TEXT, outcome TEXT NOT NULL, error TEXT,
  dry_run INTEGER NOT NULL, rerun INTEGER NOT NULL)`,
		`INSERT INTO activity (classifier, at, item_id, fields, outcome, dry_run, rerun) VALUES ('c', 1, 'old', '{}', 'recorded', 1, 0)`,
	} {
		if _, err = db.ExecContext(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

func reopen(t *testing.T, base string) {
	t.Helper()
	st, err := openState(context.Background(), base, 0o700, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	closeOrFail(t, st)
}

func closeOrFail(t *testing.T, st *stateDB) {
	t.Helper()
	if err := st.close(); err != nil {
		t.Error(err)
	}
}
