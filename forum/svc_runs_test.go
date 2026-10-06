// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Tests of forums with several runs (DESIGN.md §7.21).

// svcFinish completes the forum's live run and waits for it to settle and
// for its notice.
func svcFinish(t *testing.T, e *svcEnv, id string, notices int) {
	t.Helper()
	e.ctrls.get(t, id).finish <- StatusCompleted
	e.settled(id, StatusCompleted)
	svcEventually(t, "notice", func() bool { return e.notifier.count() == notices })
}

// svcRelaunch launches the forum again and returns the run number.
func svcRelaunch(t *testing.T, e *svcEnv, id string) int {
	t.Helper()
	n, err := e.svc.Launch(t.Context(), id, e.opts())
	if err != nil {
		t.Fatalf("launch again: %v", err)
	}
	e.ctrls.get(t, id)
	return n
}

// The configuration can be changed whenever the forum is not running: new,
// paused or ended; while it runs every change is refused and the
// configuration is left as it was.
func TestSvcConfigEditOnlyWhenNotRunning(t *testing.T) {
	e := svcSetup(t)
	id, err := e.svc.NewForum(t.Context(), e.scope)
	if err != nil {
		t.Fatal(err)
	}
	edit := func() error {
		return e.svc.UpdateConfig(t.Context(), e.scope, id, []byte(`{"brief": {"task": "Answer at length."}}`))
	}
	if err = e.svc.SetConfig(t.Context(), e.scope, id, []byte(svcSimpleJSON)); err != nil {
		t.Fatalf("set a new forum's configuration: %v", err)
	}
	if err = edit(); err != nil {
		t.Fatalf("update a new forum: %v", err)
	}
	if _, err = e.svc.Launch(t.Context(), id, e.opts()); err != nil {
		t.Fatal(err)
	}
	e.running(id)
	before, err := e.svc.ExportConfig(t.Context(), e.scope, id)
	if err != nil {
		t.Fatal(err)
	}
	for name, op := range map[string]func() error{
		"set":    func() error { return e.svc.SetConfig(t.Context(), e.scope, id, []byte(`{}`)) },
		"update": edit,
	} {
		if err := op(); !errors.Is(err, ErrInvalidState) || err.Error() != "forum "+id+" is running; pause or cancel it first" {
			t.Errorf("%s while running = %v", name, err)
		}
	}
	if after, exportErr := e.svc.ExportConfig(t.Context(), e.scope, id); exportErr != nil || string(after) != string(before) {
		t.Errorf("a refused change altered the configuration (%v):\n%s", exportErr, after)
	}
	// Interrupted (running on disk, no live controller): still running.
	e.restart()
	if err := edit(); !errors.Is(err, ErrInvalidState) {
		t.Errorf("update an interrupted forum = %v", err)
	}
	if err := e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusPaused)
	if err := edit(); err != nil {
		t.Errorf("update a paused forum: %v", err)
	}
	if err := e.svc.Cancel(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusCancelled)
	if err := e.svc.SetConfig(t.Context(), e.scope, id, []byte(svcConfigJSON)); err != nil {
		t.Errorf("set an ended forum's configuration: %v", err)
	}
}

// Each launch is a new run in its own folder; earlier runs keep their
// configuration, files and results, and status and results select a run.
func TestSvcTwoRunsKeepSeparateFoldersAndResults(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch(svcSimpleJSON)
	svcFinish(t, e, id, 1)
	first := e.store(id)
	firstSnap, err := first.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}

	sum, err := e.svc.Status(t.Context(), e.scope, id, 0)
	if err != nil || sum.ConfigChanged || sum.Runs != 1 {
		t.Errorf("status after run 1 = %+v (%v)", sum, err)
	}
	if err = e.svc.UpdateConfig(t.Context(), e.scope, id, []byte(`{"name": "second"}`)); err != nil {
		t.Fatal(err)
	}
	if sum, err = e.svc.Status(t.Context(), e.scope, id, 0); err != nil || !sum.ConfigChanged {
		t.Errorf("status after a change = %+v (%v), want config_changed", sum, err)
	}
	if n := svcRelaunch(t, e, id); n != 2 {
		t.Fatalf("second launch is run %d", n)
	}
	if sum, err = e.svc.Status(t.Context(), e.scope, id, 0); err != nil || sum.Run != 2 || sum.Runs != 2 || sum.ConfigChanged || sum.Name != "second" {
		t.Errorf("status of the second run = %+v (%v)", sum, err)
	}
	svcFinish(t, e, id, 2)

	second := e.store(id)
	if second.RunNumber() != 2 || first.Root() == second.Root() {
		t.Fatalf("runs share %s", second.Root())
	}
	for n, wantName := range map[int]string{1: "svc-simple", 2: "second"} {
		res, resErr := e.svc.Results(t.Context(), e.scope, id, n)
		if resErr != nil || res.Run != n || res.Name != wantName || res.Status != StatusCompleted {
			t.Errorf("results of run %d = %+v (%v)", n, res, resErr)
		}
		runSum, sumErr := e.svc.Status(t.Context(), e.scope, id, n)
		if sumErr != nil || runSum.Run != n || runSum.Name != wantName || runSum.Runs != 2 {
			t.Errorf("status of run %d = %+v (%v)", n, runSum, sumErr)
		}
	}
	if latest, latestErr := e.svc.Results(t.Context(), e.scope, id, 0); latestErr != nil || latest.Run != 2 {
		t.Errorf("latest results = %+v (%v)", latest, latestErr)
	}
	if _, err = e.svc.Results(t.Context(), e.scope, id, 3); !errors.Is(err, ErrInvalidState) || err.Error() != "forum "+id+" has no run 3" {
		t.Errorf("results of run 3 = %v", err)
	}
	if _, err = e.svc.Status(t.Context(), e.scope, id, 3); !errors.Is(err, ErrInvalidState) {
		t.Errorf("status of run 3 = %v", err)
	}

	// Run 1 is untouched: same snapshot, its own configuration, its result.
	again, err := first.ReadSnapshot()
	if err != nil || again.Run != 1 || again.ConfigDigest != firstSnap.ConfigDigest || again.LaunchedAt != firstSnap.LaunchedAt {
		t.Errorf("run 1's snapshot changed: %+v (%v)", again, err)
	}
	if raw, err := first.ReadConfig(); err != nil || strings.Contains(string(raw), `"second"`) {
		t.Errorf("run 1's configuration = %s (%v)", raw, err)
	}
	if raw, err := second.ReadConfig(); err != nil || !strings.Contains(string(raw), `"second"`) {
		t.Errorf("run 2's configuration = %s (%v)", raw, err)
	}
	e.notifier.mu.Lock()
	notices := slices.Clone(e.notifier.notices)
	e.notifier.mu.Unlock()
	if len(notices) != 2 || notices[0].Run != 1 || notices[1].Run != 2 {
		t.Errorf("notices = %+v", notices)
	}
	for _, n := range []int{1, 2} {
		for _, marker := range []string{CleanupAgents, cleanupNotice} {
			if _, ok := e.markerRun(id, n, marker); ok {
				t.Errorf("run %d's %s marker is left", n, marker)
			}
		}
	}
	if p := e.notifier.problems(); len(p) != 0 {
		t.Errorf("notice order: %v", p)
	}
}

// Pausing and resuming without a change works as before; once the
// configuration changes, the paused run cannot be resumed and a launch
// starts a new run, cancelling the paused one without a notice.
func TestSvcResumeAfterAConfigChange(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch("")
	e.running(id)
	pause := func() {
		t.Helper()
		if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
			t.Fatal(err)
		}
		e.settled(id, StatusPaused)
	}
	pause()
	if err := e.svc.Resume(t.Context(), e.scope, id); err != nil {
		t.Fatalf("resume without a change: %v", err)
	}
	e.running(id)
	pause()
	firstAgents := e.agents.createdIDs()

	// A change that formats to the same document is no change.
	if err := e.svc.UpdateConfig(t.Context(), e.scope, id, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if sum, err := e.svc.Status(t.Context(), e.scope, id, 0); err != nil || sum.ConfigChanged {
		t.Errorf("an empty patch counts as a change: %+v (%v)", sum, err)
	}
	if err := e.svc.UpdateConfig(t.Context(), e.scope, id, []byte(`{"brief": {"task": "Answer at length."}}`)); err != nil {
		t.Fatal(err)
	}
	err := e.svc.Resume(t.Context(), e.scope, id)
	if !errors.Is(err, ErrInvalidState) || err.Error() != "the config changed; launch to start a new run" {
		t.Fatalf("resume after a change = %v", err)
	}
	if e.status(id) != StatusPaused {
		t.Errorf("the refused resume changed the run: %s", e.status(id))
	}

	if n := svcRelaunch(t, e, id); n != 2 {
		t.Fatalf("launch after the change is run %d, want 2", n)
	}
	e.running(id)
	if sum, err := e.svc.Status(t.Context(), e.scope, id, 1); err != nil || sum.Status != StatusCancelled {
		t.Errorf("run 1 after the new launch = %+v (%v), want cancelled", sum, err)
	}
	if res, err := e.svc.Results(t.Context(), e.scope, id, 1); err != nil || res.Status != StatusCancelled {
		t.Errorf("run 1's result = %+v (%v)", res, err)
	}
	if e.notifier.count() != 0 {
		t.Errorf("%d notices for the superseded run, want none", e.notifier.count())
	}
	deleted := e.agents.deletedIDs()
	for _, agentID := range firstAgents {
		if !slices.Contains(deleted, agentID) {
			t.Errorf("run 1's temporary agent %s was not deleted", agentID)
		}
	}
	if e.keptAlive(id) {
		t.Error("the superseded run is still kept alive")
	}
	for _, marker := range []string{CleanupAgents, cleanupNotice} {
		if _, ok := e.markerRun(id, 1, marker); ok {
			t.Errorf("run 1's %s marker is left", marker)
		}
	}
	// The new run goes on as usual and is the one controlled.
	if err := e.svc.Cancel(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusCancelled)
	svcEventually(t, "run 2's notice", func() bool { return e.notifier.count() == 1 })
	if res, _ := e.notifier.last(); res.Run != 2 {
		t.Errorf("notice for run %d, want 2", res.Run)
	}
}

// A launch is refused while the latest run is running, live or interrupted.
func TestSvcLaunchRefusedWhileRunning(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch(svcSimpleJSON)
	e.running(id)
	if _, err := e.svc.Launch(t.Context(), id, e.opts()); !errors.Is(err, ErrInvalidState) || err.Error() != "forum "+id+" is running; pause or cancel it first" {
		t.Errorf("launch while running = %v", err)
	}
	e.restart()
	if _, err := e.svc.Launch(t.Context(), id, e.opts()); !errors.Is(err, ErrInvalidState) {
		t.Errorf("launch while interrupted = %v", err)
	}
	if latest, err := latestRun(e.store(id)); err != nil || latest != 1 {
		t.Errorf("latest run = %d (%v), want 1", latest, err)
	}
}

// Delete removes the forum with every run, and the temporary agents and
// markers of each run; it is refused while the latest run is running.
func TestSvcDeleteRemovesEveryRun(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch("")
	svcFinish(t, e, id, 1)
	svcRelaunch(t, e, id)
	e.running(id)
	if err := e.svc.Delete(t.Context(), e.scope, id); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("delete while running = %v", err)
	}
	if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusPaused)
	// Run 1's deletion of its agents failed once; its marker is left.
	if err := setAgentsMarker(e.store(id).Run(1), []string{"left-by-run-1"}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(t.Context(), e.scope, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.scope.BaseDirectory, id)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the forum's directory is left: %v", err)
	}
	created, deleted := e.agents.createdIDs(), e.agents.deletedIDs()
	for _, agentID := range append(created, "left-by-run-1") {
		if !slices.Contains(deleted, agentID) {
			t.Errorf("agent %s was not deleted", agentID)
		}
	}
	entries, err := os.ReadDir(filepath.Join(e.scope.BaseDirectory, dirCleanup))
	if err != nil || len(entries) != 0 {
		t.Errorf("cleanup entries left: %v (%v)", entries, err)
	}
	if e.keptAlive(id) {
		t.Error("the deleted forum is still kept alive")
	}
}

// Recovery resumes an interrupted latest run, leaves the earlier run as it
// is, and finishes an earlier run's pending cleanup.
func TestSvcRecoverAnInterruptedRun(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch(svcSimpleJSON)
	svcFinish(t, e, id, 1)
	// Run 1's agents could not be deleted before the restart.
	run1 := e.store(id)
	if err := setAgentsMarker(run1, []string{"left-by-run-1"}); err != nil {
		t.Fatal(err)
	}
	svcRelaunch(t, e, id)
	e.running(id)
	e.restart()
	opens := e.ctrls.openCount()
	if err := e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
		t.Fatal(err)
	}
	e.running(id)
	if r, ok := e.svc.running(e.scope, id); !ok || r.store.RunNumber() != 2 {
		t.Fatalf("the resumed run is not run 2: %v", ok)
	}
	if e.ctrls.openCount() != opens+1 {
		t.Errorf("opened %d controllers at recovery, want only run 2's", e.ctrls.openCount()-opens)
	}
	if sum, err := e.svc.Status(t.Context(), e.scope, id, 1); err != nil || sum.Status != StatusCompleted {
		t.Errorf("run 1 = %+v (%v)", sum, err)
	}
	if !slices.Contains(e.agents.deletedIDs(), "left-by-run-1") {
		t.Error("run 1's pending agent was not deleted at recovery")
	}
	if e.notifier.count() != 1 {
		t.Errorf("%d notices, want run 1's only", e.notifier.count())
	}
}

// A crash while a launch was replacing a paused run (the new run written,
// the old one not yet cancelled) is finished at recovery: the old run is
// cancelled without a notice and the new one resumes.
func TestSvcRecoverASupersedeCutShort(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch(svcSimpleJSON)
	if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusPaused)
	e.restart()
	// What launchLocked writes before it supersedes run 1.
	f, err := OpenStore(e.scope.BaseDirectory, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Lock(); err != nil {
		t.Fatal(err)
	}
	raw, _, err := f.ReadForumConfig()
	if err != nil {
		t.Fatal(err)
	}
	opts := e.opts()
	opts.Origin.AgentID = e.scope.AgentID
	cfg, resolved, err := e.svc.check(t.Context(), raw, opts)
	if err != nil {
		t.Fatal(err)
	}
	run2, err := f.CreateRun(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.allocate(t.Context(), run2, raw, cfg, resolved, opts); err != nil {
		t.Fatal(err)
	}
	f.Unlock()

	if err := e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
		t.Fatal(err)
	}
	e.running(id)
	if sum, err := e.svc.Status(t.Context(), e.scope, id, 1); err != nil || sum.Status != StatusCancelled {
		t.Errorf("run 1 = %+v (%v), want cancelled", sum, err)
	}
	if e.notifier.count() != 0 {
		t.Errorf("%d notices, want none", e.notifier.count())
	}
	if e.keptAlive(id) {
		t.Error("the superseded run is kept alive")
	}
}

// The book: chapter 1 runs, the same forum's source is pointed at chapter
// 2, and the next launch reviews chapter 2 while run 1 keeps chapter 1.
func TestSvcBookChapterPerRun(t *testing.T) {
	e := svcSetup(t)
	if err := os.WriteFile(filepath.Join(e.workspace, "chapter2.md"), []byte("# Chapter 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, _ := e.launch("")
	svcFinish(t, e, id, 1)
	if err := e.svc.UpdateConfig(t.Context(), e.scope, id, []byte(`{"sources": {"doc": {"file": "chapter2.md"}}}`)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ValidateConfig(t.Context(), id, e.opts()); err != nil {
		t.Fatal(err)
	}
	svcRelaunch(t, e, id)
	for n, want := range map[int]string{1: "# Doc\n\nBody.\n", 2: "# Chapter 2\n"} {
		f, err := OpenStore(e.scope.BaseDirectory, id)
		if err != nil {
			t.Fatal(err)
		}
		run := f.Run(n)
		snap, err := run.ReadSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		if data, err := run.ReadFile(snap.Sources["doc"].File); err != nil || string(data) != want {
			t.Errorf("run %d reviewed %q (%v), want %q", n, data, err, want)
		}
	}
}

// The run argument of status and results selects a run; anything but a
// positive whole number is refused, and run needs an id.
func TestSvcToolRunArgument(t *testing.T) {
	st := svcToolSetup(t)
	id := st.launch()
	st.stopped(id)
	if out := st.ok("launch", map[string]any{"id": id}); out != "Forum "+id+" launched (run 2)." {
		t.Fatalf("second launch = %q", out)
	}
	st.e.running(id)

	var sum Summary
	if err := json.Unmarshal([]byte(st.ok("status", map[string]any{"id": id, "run": 1.0})), &sum); err != nil ||
		sum.Run != 1 || sum.Runs != 2 || sum.Status != StatusCancelled {
		t.Errorf("status of run 1 = %+v (%v)", sum, err)
	}
	if err := json.Unmarshal([]byte(st.ok("status", map[string]any{"id": id})), &sum); err != nil || sum.Run != 2 || sum.Status != StatusRunning {
		t.Errorf("status of the latest run = %+v (%v)", sum, err)
	}
	var res ResultsView
	if err := json.Unmarshal([]byte(st.ok("results", map[string]any{"id": id, "run": 1.0})), &res); err != nil ||
		res.Run != 1 || res.Transcript != "forums/"+id+"/runs/1/transcript.md" {
		t.Errorf("results of run 1 = %+v (%v)", res, err)
	}
	if msg := st.refused("results", map[string]any{"id": id, "run": 3.0}, "no run"); msg != "Forum "+id+" has no run 3." {
		t.Errorf("results of run 3 = %q", msg)
	}
	for _, bad := range []any{0.0, -1.0, 1.5, "1", true} {
		st.refused("status", map[string]any{"id": id, "run": bad}, "The run argument must be a run number: 1, 2, 3 and so on.")
		st.refused("results", map[string]any{"id": id, "run": bad}, "The run argument must be a run number")
	}
	st.refused("status", map[string]any{"run": 1.0}, "The run argument needs the id of a forum.")
	if msg := st.refused("resume", map[string]any{"id": uuid.NewString()}, "was not found"); !strings.HasSuffix(msg, "was not found.") {
		t.Errorf("resume unknown = %q", msg)
	}
}
