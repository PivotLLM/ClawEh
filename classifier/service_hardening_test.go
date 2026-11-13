// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PivotLLM/toolspec"

	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
)

// restartedService is a second service over e's fakes and folder, as after
// a restart of the process.
func (e *env) restartedService() *Service {
	e.t.Helper()
	svc := New(Host{
		Tools: e.tools, Model: e.model, Notifier: e.notifier, Auditor: e.auditor, Logger: e.log, Clock: e.clock,
		Modes:    func(string) (os.FileMode, os.FileMode) { return 0o750, 0o640 },
		Ceilings: func() Ceilings { e.mu.Lock(); defer e.mu.Unlock(); return e.ceilings },
		Scopes:   func() []Scope { e.mu.Lock(); defer e.mu.Unlock(); return append([]Scope(nil), e.scopes...) },
	})
	e.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.Close(ctx); err != nil {
			e.t.Errorf("Close: %v", err)
		}
	})
	return svc
}

// A shutdown in the middle of a run lets the item in flight finish (its
// action runs and is recorded), starts no other item, and records the run as
// stopped; after a restart the next run takes the items not handled and never
// repeats the action already run.
func TestCloseMidRunResumesAfterRestart(t *testing.T) {
	e := newEnv(t)
	def := definition()
	def["limits"] = map[string]any{"parallel": 1}
	e.create(def)

	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	e.model.setReply(func(string, string) (string, error) {
		entered <- struct{}{}
		<-release
		return "urgent", nil
	})
	e.start()
	<-entered // m1 is being classified

	closed := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- e.svc.Close(ctx)
	}()
	eventually(t, "Close to begin", func() bool {
		e.svc.mu.Lock()
		defer e.svc.mu.Unlock()
		return e.svc.closing
	})
	select {
	case err := <-closed:
		t.Fatalf("Close returned before the item in flight finished: %v", err)
	default:
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := e.moves(); len(got) != 1 || got["m1"] != "Urgent" {
		t.Fatalf("moves after shutdown = %v, want m1 only", got)
	}
	if _, err := e.svc.List(e.scope); err != nil {
		t.Fatalf("List after Close: %v", err)
	}
	if _, err := e.svc.Status(e.scope, "inbox-priority"); !errors.Is(err, ErrClosed) {
		t.Errorf("Status after Close = %v, want ErrClosed", err)
	}

	// Restart: a new service over the same folder.
	e.model.setReply(func(string, string) (string, error) { return "urgent", nil })
	svc := e.restartedService()
	sums, err := svc.List(e.scope)
	if err != nil || len(sums) != 1 || sums[0].LastRun == nil {
		t.Fatalf("List after restart = %+v, %v", sums, err)
	}
	if r := sums[0].LastRun; r.Status != RunStopped || r.Error != "the service stopped" || r.Acted != 1 || r.NewItems != 3 {
		t.Errorf("interrupted run = %+v, want stopped by the service after one item", r)
	}
	d, err := svc.validDefinition(e.scope, "inbox-priority")
	if err != nil {
		t.Fatal(err)
	}
	rec := svc.runClassifier(context.Background(), e.scope, d, nil)
	if rec.Status != RunOK || rec.NewItems != 2 || rec.Acted != 2 {
		t.Errorf("run after restart = %+v, want the two items not handled", rec)
	}
	calls := map[string]int{}
	for _, c := range e.tools.callsOf("mail_move") {
		calls[fmt.Sprint(c.args["id"])]++
	}
	if calls["m1"] != 1 || calls["m2"] != 1 || calls["m3"] != 1 {
		t.Errorf("actions per item = %v, want each once", calls)
	}
}

// Start after Close starts nothing; a second Start does nothing.
func TestStartAfterCloseAndTwice(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	if err := e.svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.svc.Start(context.Background())
	e.svc.mu.Lock()
	started := e.svc.started
	e.svc.mu.Unlock()
	if started {
		t.Fatal("Start after Close started the scheduler")
	}

	e2 := newEnv(t)
	e2.create(definition())
	e2.start()
	eventually(t, "the first run", func() bool { return e2.lists() == 2 })
	e2.settle()
	e2.start() // again: no second loop, no extra run
	e2.settle()
	if n := e2.lists(); n != 2 {
		t.Errorf("source calls after a second Start = %d, want 2", n)
	}
}

// A Delete whose wait for the run lock is cancelled removes nothing.
func TestDeleteCancelledWhileWaitingKeepsTheClassifier(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	e.model.setReply(func(string, string) (string, error) {
		entered <- struct{}{}
		<-release
		return "low", nil
	})
	rerun := make(chan error, 1)
	go func() {
		_, err := e.svc.Rerun(context.Background(), e.scope, "inbox-priority", "m2")
		rerun <- err
	}()
	<-entered // the rerun holds the classifier's lock
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.svc.Delete(ctx, e.scope, "inbox-priority"); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-rerun; err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if _, err := e.svc.Get(e.scope, "inbox-priority"); err != nil {
		t.Errorf("the classifier is gone after a cancelled Delete: %v", err)
	}
	if got := e.processed("inbox-priority")["m2"].status; got != StatusActed {
		t.Errorf("rerun item status = %q, want acted", got)
	}
}

// Rerun and Test refuse an unknown classifier, an invalid one, an empty
// source and an item the source does not list; a Rerun whose wait is
// cancelled returns the cancellation.
func TestTestAndRerunRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.Test(ctx, e.scope, "nope", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("Test unknown = %v", err)
	}
	if _, err := e.svc.Rerun(ctx, e.scope, "nope", "m1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Rerun unknown = %v", err)
	}
	e.create(definition())
	if _, err := e.svc.Test(ctx, e.scope, "inbox-priority", "zz"); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("Test unknown item = %v", err)
	}
	d, err := e.svc.Test(ctx, e.scope, "inbox-priority", "")
	if err != nil || d.ItemID != "m1" || d.Category != "urgent" || d.Action != "mail_move" {
		t.Errorf("Test first item = %+v, %v", d, err)
	}
	e.setInbox(nil)
	if _, err := e.svc.Test(ctx, e.scope, "inbox-priority", ""); !errors.Is(err, ErrNoItems) {
		t.Errorf("Test empty source = %v", err)
	}
	e.tools.remove("mail_read")
	if _, err := e.svc.Test(ctx, e.scope, "inbox-priority", ""); err == nil || !strings.Contains(err.Error(), "mail_read") {
		t.Errorf("Test without the fetch tool = %v", err)
	}
	if len(e.tools.callsOf("mail_move")) != 0 {
		t.Error("Test ran an action")
	}

	// A hand edit that breaks the file: refused with the reason.
	path := definitionPath(e.scope.BaseDirectory, "inbox-priority")
	if err := os.WriteFile(path, []byte(`{"id":"inbox-priority","owner":"alice","surprise":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var ve *ValidationError
	if _, err := e.svc.Test(ctx, e.scope, "inbox-priority", ""); !errors.As(err, &ve) || !strings.Contains(err.Error(), "is not valid") {
		t.Errorf("Test invalid = %v", err)
	}
	if _, err := e.svc.Rerun(ctx, e.scope, "inbox-priority", "m1"); !errors.As(err, &ve) {
		t.Errorf("Rerun invalid = %v", err)
	}
}

// Update refuses changes that are not an object, an id change and a
// classifier of another agent, and leaves the file as it was.
func TestUpdateRefusals(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	ctx := context.Background()
	before, getErr := e.svc.Get(e.scope, "inbox-priority")
	if getErr != nil {
		t.Fatal(getErr)
	}
	for _, tc := range []struct {
		name, patch, want string
	}{
		{"array", `[1]`, "The changes must be a JSON object."},
		{"not JSON", `{`, "The changes must be a JSON object."},
		{"id change", `{"id":"other"}`, "id cannot be changed; create a new classifier instead."},
		{"unknown key", `{"surprise":1}`, "surprise"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := e.svc.Update(ctx, e.scope, "inbox-priority", []byte(tc.patch))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Update(%s) = %v, want %q", tc.patch, err, tc.want)
			}
		})
	}
	bob := Scope{AgentID: "bob", BaseDirectory: e.scope.BaseDirectory}
	if err := e.svc.Update(ctx, bob, "inbox-priority", []byte(`{"enabled":false}`)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update by bob = %v, want ErrNotFound", err)
	}
	if err := e.svc.Delete(ctx, bob, "inbox-priority"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete by bob = %v, want ErrNotFound", err)
	}
	if _, err := e.svc.Status(bob, "inbox-priority"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Status by bob = %v, want ErrNotFound", err)
	}
	if _, err := e.svc.Activity(bob, "inbox-priority", ActivityQuery{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Activity by bob = %v, want ErrNotFound", err)
	}
	after, err := e.svc.Get(e.scope, "inbox-priority")
	if err != nil || string(after) != string(before) {
		t.Errorf("file changed by refused updates:\n%s\nwant\n%s", after, before)
	}
}

// Status of every classifier includes an invalid one with why; with no
// classifier at all nothing is created in the folder.
func TestStatusAll(t *testing.T) {
	e := newEnv(t)
	statuses, statusErr := e.svc.Status(e.scope, "")
	if statusErr != nil || len(statuses) != 0 {
		t.Fatalf("Status with none = %+v, %v", statuses, statusErr)
	}
	if _, err := os.Stat(filepath.Join(e.scope.BaseDirectory, classifierfs.StateFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Status with none created the state: %v", err)
	}
	e.create(definition())
	e.run("inbox-priority")
	if err := os.WriteFile(filepath.Join(e.scope.BaseDirectory, "broken.json"),
		[]byte(`{"id":"broken","owner":"alice"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	statuses, statusErr = e.svc.Status(e.scope, "")
	if statusErr != nil || len(statuses) != 2 {
		t.Fatalf("Status all = %+v, %v", statuses, statusErr)
	}
	byID := map[string]Status{}
	for _, s := range statuses {
		byID[s.ID] = s
	}
	if s := byID["broken"]; s.Invalid == "" || s.LastRun != nil {
		t.Errorf("broken status = %+v", s)
	}
	if s := byID["inbox-priority"]; s.LastRun == nil || s.Counts[StatusActed] != 3 || len(s.Recent) != 3 {
		t.Errorf("inbox status = %+v", s)
	}
}

// Every tool refuses a bad argument in one sentence, names the classifier it
// cannot find, and turns an internal failure into a sentence without the
// error chain (the error is logged).
func TestToolRefusals(t *testing.T) {
	e := newEnv(t)
	defs := Tools(e.svc, fakeToolHost{scope: e.scope})
	e.create(definition())
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"get", map[string]any{}, "id is required."},
		{"get", map[string]any{"id": "  "}, "id is required."},
		{"get", map[string]any{"id": 3.0}, "id must be a string."},
		{"delete", map[string]any{"id": "nope"}, "Classifier nope does not exist."},
		{"create", map[string]any{"definition": "x"}, "definition must be a JSON object."},
		{"validate", map[string]any{}, "definition must be a JSON object."},
		{"validate", map[string]any{"definition": map[string]any{"id": "x", "surprise": 1}}, "surprise"},
		{"update", map[string]any{"id": "inbox-priority", "changes": []any{}}, "changes must be a JSON object."},
		{"status", map[string]any{"id": 1.0}, "id must be a string."},
		{"status", map[string]any{"id": "nope"}, "Classifier nope does not exist."},
		{"test", map[string]any{"id": "inbox-priority", "item_id": true}, "item_id must be a string."},
		{"test", map[string]any{"id": "inbox-priority", "item_id": "zz"}, "The source does not list that item."},
		{"rerun", map[string]any{"id": "inbox-priority"}, "item_id is required."},
		{"activity", map[string]any{"id": "inbox-priority", "offset": 1.5}, "offset must be a whole number, 0 or more."},
		{"activity", map[string]any{"id": "inbox-priority", "search": 2.0}, "search must be a string."},
		{"activity", map[string]any{"id": "nope"}, "Classifier nope does not exist."},
		{"activity", map[string]any{"id": "inbox-priority", "outcome": "classified"}, "outcome must be acted, recorded, unclassified, failed, run_failed."},
		{"activity", map[string]any{"id": "inbox-priority", "outcome": 1.0}, "outcome must be a string."},
		{"list", map[string]any{"a": 1, "b": 2}, "Unknown arguments a, b; this tool takes no arguments."},
		{"rerun", map[string]any{"id": "x", "item": "m1"}, "Unknown argument item; use id or item_id."},
	} {
		res := call(t, defs, tc.tool, tc.args)
		if !res.IsError || !strings.Contains(res.ForLLM, tc.want) {
			t.Errorf("%s(%v) = %q, want an error with %q", tc.tool, tc.args, res.ForLLM, tc.want)
		}
	}
	if len(e.tools.callsOf("mail_move")) != 0 {
		t.Error("a refused call ran an action")
	}

	// An empty source, through the tool.
	e.setInbox(nil)
	if res := call(t, defs, "test", map[string]any{"id": "inbox-priority"}); !res.IsError || res.ForLLM != "The source lists no items." {
		t.Errorf("test on an empty source = %q", res.ForLLM)
	}
	// Status of all, through the tool, is a list.
	if res := call(t, defs, "status", map[string]any{}); res.IsError || !strings.HasPrefix(strings.TrimSpace(res.ForLLM), "<<untrusted>>[") {
		t.Errorf("status of all = %q", res.ForLLM)
	}

	// An internal failure: the folder is a file.
	file := filepath.Join(t.TempDir(), "not-a-folder")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.log.expectErrors()
	broken := Tools(e.svc, fakeToolHost{scope: Scope{AgentID: "alice", BaseDirectory: file}})
	res, err := toolByName(t, broken, "list").Handler(&toolspec.ToolCall{Ctx: context.Background(), Args: map[string]any{}})
	if err != nil || !res.IsError || res.ForLLM != "The operation failed because of an internal error." || res.Err == nil {
		t.Errorf("list on a broken folder = %+v, %v", res, err)
	}
	if strings.Contains(res.ForLLM, file) {
		t.Error("the internal error leaked a path to the agent")
	}
	if len(e.log.errors()) != 1 {
		t.Errorf("internal error logged %d times, want once", len(e.log.errors()))
	}

	// Every tool returns the scope refusal (the switch is off) as the error.
	refused := Tools(e.svc, fakeToolHost{err: errors.New("classifier tools are not enabled for agent alice")})
	for _, d := range refused {
		if _, err := d.Handler(&toolspec.ToolCall{Ctx: context.Background(), Args: map[string]any{}}); err == nil {
			t.Errorf("%s ran with the switch off", d.Name)
		}
	}
}

// A field the model reads that holds an object or a number is shown as
// compact JSON, not dropped.
func TestRunShowsNonStringFieldsAsJSON(t *testing.T) {
	e := newEnv(t)
	def := definition()
	obj(def["fetch"])["fields"] = map[string]any{"from": "from", "subject": "subject"}
	delete(obj(def["fetch"]), "html")
	e.create(def)
	e.tools.set("mail_read", func(map[string]any) (string, error) {
		return `{"from":{"address":"bob@example.com","name":"Bob <B>"},"subject":42}`, nil
	})
	e.run("inbox-priority")
	e.model.mu.Lock()
	user := e.model.calls[0]
	e.model.mu.Unlock()
	for _, want := range []string{`{"address":"bob@example.com","name":"Bob <B>"}`, "42"} {
		if !strings.Contains(user, want) {
			t.Errorf("model input lacks %s:\n%s", want, user)
		}
	}
}

// The scheduler leaves alone a file owned by an agent without the switch,
// stops following a classifier handed to such an agent by a hand edit, and
// reports a file without an owner as not run; a folder that cannot be
// listed is logged.
func TestSchedulerOwnersAndUnlistableFolder(t *testing.T) {
	e := newEnv(t)
	e.create(definition())
	carol := definition()
	carol["id"] = "carols"
	carol["owner"] = "carol"
	if err := os.WriteFile(filepath.Join(e.scope.BaseDirectory, "carols.json"), mustJSON(t, carol), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.scope.BaseDirectory, "ownerless.json"), []byte(`{"id":"ownerless"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.start()
	eventually(t, "the first run", func() bool { return e.lists() == 2 })
	e.settle()
	e.svc.mu.Lock()
	_, carolKnown := e.svc.entries[entryKey(e.scope.BaseDirectory, "carols")]
	ownerless := e.svc.entries[entryKey(e.scope.BaseDirectory, "ownerless")]
	e.svc.mu.Unlock()
	if carolKnown {
		t.Error("a classifier of an agent without the switch is followed")
	}
	if ownerless == nil || ownerless.def != nil || ownerless.scope.AgentID != "alice" {
		t.Errorf("ownerless entry = %+v, want reported, not run, in alice's scope", ownerless)
	}
	if e.log.warnings("ownerless.json") == 0 {
		t.Error("the ownerless file was not logged")
	}

	// A hand edit gives alice's classifier to carol: it is no longer run.
	path := definitionPath(e.scope.BaseDirectory, "inbox-priority")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), `"owner": "alice"`, `"owner": "carol"`, 1)
	if edited == string(raw) {
		t.Fatalf("owner not found in %s", raw)
	}
	if err := os.WriteFile(path, []byte(edited+" "), 0o600); err != nil {
		t.Fatal(err)
	}
	e.svc.wake()
	eventually(t, "the entry to go", func() bool {
		e.svc.mu.Lock()
		defer e.svc.mu.Unlock()
		_, ok := e.svc.entries[entryKey(e.scope.BaseDirectory, "inbox-priority")]
		return !ok
	})
	e.settle()
	lists := e.lists()
	e.clock.Advance(10 * time.Minute)
	e.settle()
	if e.lists() != lists {
		t.Error("a classifier handed to another agent still runs")
	}

	// A folder that cannot be listed.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.svc.scanBase(context.Background(), file, []Scope{{AgentID: "alice", BaseDirectory: file}}, map[string]bool{})
	if e.log.warnings("cannot be listed") == 0 {
		t.Error("an unlistable folder was not logged")
	}
}

// Folders and files are created with exactly the modes asked for, whatever
// the umask; a file where a folder belongs is refused.
func TestAtomicFilesExactModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	root := t.TempDir()

	dir := filepath.Join(root, "a", "b")
	if err := mkdirAllExact(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(root, "a"), dir} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o750 {
			t.Errorf("%s mode = %v, %v; want 0750", d, fi.Mode().Perm(), err)
		}
	}
	if err := mkdirAllExact(dir, 0o700); err != nil {
		t.Errorf("existing folder: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o750 {
		t.Error("an existing folder's mode was changed")
	}
	file := filepath.Join(root, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mkdirAllExact(filepath.Join(file, "x"), 0o700); err == nil {
		t.Error("a folder under a file was accepted")
	}
	if err := mkdirAllExact(file, 0o700); !errors.Is(err, fs.ErrExist) {
		t.Errorf("a file in place of the folder = %v, want ErrExist", err)
	}

	// writeAtomic: exclusive refuses an existing file and leaves no
	// temporary file; a replace keeps the exact mode.
	path := filepath.Join(dir, "c.json")
	if err := writeAtomic(path, []byte("one"), 0o640, true); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(path, []byte("two"), 0o640, true); !errors.Is(err, fs.ErrExist) {
		t.Errorf("exclusive over an existing file = %v, want ErrExist", err)
	}
	if err := writeAtomic(path, []byte("three"), 0o640, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "three" {
		t.Errorf("content = %q, %v", data, err)
	}
	if fi, statErr := os.Stat(path); statErr != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("file mode = %v, want 0640", fi.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), classifierfs.TempPrefix) {
			t.Errorf("temporary file left: %s", en.Name())
		}
	}
	if err := writeAtomic(filepath.Join(root, "missing", "x.json"), []byte("x"), 0o600, false); err == nil {
		t.Error("write into a missing folder succeeded")
	}
	if err := syncDir(filepath.Join(root, "missing")); err == nil {
		t.Error("sync of a missing folder succeeded")
	}
}

// The state folder and database take the host's modes, and a host that
// gives none gets 0700/0600.
func TestStateModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	for _, tc := range []struct {
		name          string
		modes         func(string) (os.FileMode, os.FileMode)
		wantDir, want os.FileMode
	}{
		{"host modes", func(string) (os.FileMode, os.FileMode) { return 0o750, 0o640 }, 0o750, 0o640},
		{"none", nil, 0o700, 0o600},
		{"zero", func(string) (os.FileMode, os.FileMode) { return 0, 0 }, 0o700, 0o600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(Host{Modes: tc.modes, Logger: &testLogger{t: t}})
			defer func() {
				if err := svc.Close(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			base := filepath.Join(t.TempDir(), "classifiers")
			if _, err := svc.state(context.Background(), base); err != nil {
				t.Fatal(err)
			}
			if fi, err := os.Stat(base); err != nil || fi.Mode().Perm() != tc.wantDir {
				t.Errorf("folder mode = %v, want %o", fi.Mode().Perm(), tc.wantDir)
			}
			if fi, err := os.Stat(filepath.Join(base, classifierfs.StateFile)); err != nil || fi.Mode().Perm() != tc.want {
				t.Errorf("state mode = %v, want %o", fi.Mode().Perm(), tc.want)
			}
		})
	}
	// A state folder that cannot be made fails the run, which says so.
	e := newEnv(t)
	e.create(definition())
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.log.expectErrors()
	rec := e.svc.runClassifier(context.Background(), Scope{AgentID: "alice", BaseDirectory: filepath.Join(file, "sub")}, e.def(), nil)
	if rec.Status != RunError || rec.Error != "the classifier's state could not be opened" {
		t.Errorf("run without state = %+v", rec)
	}
}
