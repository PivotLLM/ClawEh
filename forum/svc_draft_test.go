// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A draft survives a restart: Recover leaves it as it is, it keeps its
// configuration and is still launched under its ID.
func TestSvcDraftSurvivesRestart(t *testing.T) {
	e := svcSetup(t)
	id, err := e.svc.NewDraft(t.Context(), e.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.SetDraftConfig(t.Context(), e.scope, id, []byte(svcSimpleJSON)); err != nil {
		t.Fatal(err)
	}
	before, err := e.svc.ExportConfig(t.Context(), e.scope, id)
	if err != nil {
		t.Fatal(err)
	}
	e.restart()
	if err = e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if e.ctrls.openCount() != 0 || len(e.stuck.list()) != 0 {
		t.Error("Recover opened or reported the draft")
	}
	if e.status(id) != StatusDraft {
		t.Errorf("status after restart = %s, want draft", e.status(id))
	}
	after, err := e.svc.ExportConfig(t.Context(), e.scope, id)
	if err != nil || string(after) != string(before) {
		t.Errorf("configuration after restart = %s (%v), want %s", after, err, before)
	}
	if err := e.svc.Launch(t.Context(), id, e.opts()); err != nil {
		t.Fatalf("launch after restart: %v", err)
	}
	e.ctrls.get(t, id)
	if got := e.status(id); got == StatusDraft {
		t.Error("still a draft after its launch")
	}
}

// A draft locked by another process (its launch in progress there) cannot
// be changed or launched here.
func TestSvcDraftLockedElsewhere(t *testing.T) {
	e := svcSetup(t)
	id, err := e.svc.NewDraft(t.Context(), e.scope)
	if err != nil {
		t.Fatal(err)
	}
	other := e.store(id)
	if err := other.Lock(); err != nil {
		t.Fatal(err)
	}
	defer other.Unlock()
	if err := e.svc.SetDraftConfig(t.Context(), e.scope, id, []byte(svcSimpleJSON)); !errors.Is(err, ErrLocked) {
		t.Errorf("set config = %v, want ErrLocked", err)
	}
	if err := e.svc.UpdateDraft(t.Context(), e.scope, id, []byte(`{"name":"x"}`)); !errors.Is(err, ErrLocked) {
		t.Errorf("update = %v, want ErrLocked", err)
	}
	if err := e.svc.Launch(t.Context(), id, e.opts()); !errors.Is(err, ErrLocked) {
		t.Errorf("launch = %v, want ErrLocked", err)
	}
}

// Only drafts can be changed; an edit refused on a launched forum leaves
// its configuration as it was launched.
func TestSvcDraftEditsRefusedOnceLaunched(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch(svcSimpleJSON)
	before, err := e.svc.ExportConfig(t.Context(), e.scope, id)
	if err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"set":    e.svc.SetDraftConfig(t.Context(), e.scope, id, []byte(`{}`)),
		"update": e.svc.UpdateDraft(t.Context(), e.scope, id, []byte(`{"name":"x"}`)),
	} {
		if !errors.Is(err, ErrInvalidState) || !strings.Contains(err.Error(), "export its config into a new forum") {
			t.Errorf("%s on a launched forum = %v", name, err)
		}
	}
	if after, err := e.svc.ExportConfig(t.Context(), e.scope, id); err != nil || string(after) != string(before) {
		t.Errorf("configuration changed: %s (%v)", after, err)
	}
	if err := e.svc.SetDraftConfig(t.Context(), e.scope, id, []byte(`[1]`)); err == nil {
		t.Error("a configuration that is not an object was accepted")
	}
}

// Delete removes a draft, its directory and its markers, like any forum.
func TestSvcDeleteDraft(t *testing.T) {
	e := svcSetup(t)
	id, err := e.svc.NewDraft(t.Context(), e.scope)
	if err != nil {
		t.Fatal(err)
	}
	keep, _ := e.launch(svcSimpleJSON)
	if err := e.svc.Delete(t.Context(), e.scope, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ids := e.forumIDs(); !slices.Equal(ids, []string{keep}) {
		t.Errorf("forums = %v, want only %s", ids, keep)
	}
	if _, err := e.svc.Status(t.Context(), e.scope, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("status after delete = %v", err)
	}
}

// errCrash is the failure resetHook injects in place of a crash.
var errCrash = errors.New("simulated crash")

// draftFiles lists the files left under a forum's root, by relative path.
func draftFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, relErr := filepath.Rel(root, p)
			out = append(out, rel)
			return relErr
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A reset to the draft interrupted after any of its removals still leaves
// a usable draft: the next recovery finishes it without reporting the
// forum, the folder holds only draft.json, and the draft can be launched
// or deleted.
func TestSvcResetToDraftSurvivesACrashAtEveryStep(t *testing.T) {
	t.Cleanup(func() { resetHook = nil })
	for step := 1; ; step++ {
		e := svcSetup(t)
		e.ctrls.openErr = ErrCorrupt // the launch fails after it has committed
		calls := 0
		resetHook = func(string) error {
			if calls++; calls == step {
				return errCrash
			}
			return nil
		}
		id, err := svcLaunch(t, e.svc, svcConfigJSON, e.opts())
		resetHook = nil
		if !errors.Is(err, errCrash) {
			if step < 3 {
				t.Fatalf("the reset had only %d steps (launch: %v)", step-1, err)
			}
			return // every step has been interrupted once
		}
		e.restart()
		e.ctrls.openErr = nil
		if err := e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
			t.Fatalf("step %d: recover: %v", step, err)
		}
		if calls := e.stuck.list(); len(calls) != 0 {
			t.Fatalf("step %d: reported %q", step, calls)
		}
		if e.status(id) != StatusDraft {
			t.Fatalf("step %d: status %s, want draft", step, e.status(id))
		}
		if got := draftFiles(t, e.store(id).Root()); !slices.Equal(got, []string{fileDraft}) {
			t.Errorf("step %d: files left %v", step, got)
		}
		if step%2 == 0 {
			if err := e.svc.Delete(t.Context(), e.scope, id); err != nil {
				t.Errorf("step %d: delete: %v", step, err)
			}
			continue
		}
		if err := e.svc.Launch(t.Context(), id, e.opts()); err != nil {
			t.Errorf("step %d: launch: %v", step, err)
		}
	}
}

// A draft missing a required subdirectory (a reset or a creation cut
// short) opens as a draft; the subdirectory is recreated.
func TestOpenStoreRecreatesADraftsSubdirectories(t *testing.T) {
	e := svcSetup(t)
	id, err := e.svc.NewDraft(t.Context(), e.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(e.scope.BaseDirectory, id, dirCommits)); err != nil {
		t.Fatal(err)
	}
	if e.status(id) != StatusDraft {
		t.Errorf("status = %s", e.status(id))
	}
	if fi, err := os.Stat(filepath.Join(e.scope.BaseDirectory, id, dirCommits)); err != nil || !fi.IsDir() {
		t.Errorf("commits/ not recreated: %v", err)
	}
}

// Text with HTML characters is kept as written in draft.json, forum.json
// and the export.
func TestSvcConfigKeepsHTMLCharacters(t *testing.T) {
	e := svcSetup(t)
	cfg := strings.Replace(svcSimpleJSON, `"Answer briefly."`, `"Q&A <b>"`, 1)
	id, err := svcLaunch(t, e.svc, cfg, e.opts())
	if err != nil {
		t.Fatal(err)
	}
	e.ctrls.get(t, id)
	exported, err := e.svc.ExportConfig(t.Context(), e.scope, id)
	if err != nil || !strings.Contains(string(exported), `"Q&A <b>"`) {
		t.Errorf("export = %s (%v)", exported, err)
	}
	raw, err := e.store(id).ReadConfig()
	if err != nil || !strings.Contains(string(raw), `"Q&A <b>"`) {
		t.Errorf("forum.json = %s (%v)", raw, err)
	}
	draft, err := e.svc.NewDraft(t.Context(), e.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.UpdateDraft(t.Context(), e.scope, draft, []byte(`{"name": "Q&A <b>"}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(e.scope.BaseDirectory, draft, fileDraft))
	if err != nil || !strings.Contains(string(data), `"Q&A <b>"`) {
		t.Errorf("draft.json = %s (%v)", data, err)
	}
}

// An agent a failed launch could not delete stays in the agents marker
// when the draft is launched again, so it is still deleted with the forum.
func TestSvcRelaunchKeepsUndeletedAgents(t *testing.T) {
	e := svcSetup(t)
	id, err := e.svc.NewDraft(t.Context(), e.scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.SetDraftConfig(t.Context(), e.scope, id, []byte(svcConfigJSON)); err != nil {
		t.Fatal(err)
	}
	if err = setAgentsMarker(e.store(id), []string{"leftover"}); err != nil {
		t.Fatal(err)
	}
	e.agents.setDeleteErr("leftover", errSvcHost)
	if err = e.svc.Launch(t.Context(), id, e.opts()); err != nil {
		t.Fatalf("launch: %v", err)
	}
	ctrl := e.ctrls.get(t, id)
	marker, ok := e.marker(id, CleanupAgents)
	if !ok || !strings.Contains(marker, `"leftover"`) || len(e.agents.createdIDs()) != 2 {
		t.Fatalf("agents marker = %s, want leftover and the two new agents", marker)
	}
	for _, created := range e.agents.createdIDs() {
		if !strings.Contains(marker, created) {
			t.Errorf("agents marker %s lacks %s", marker, created)
		}
	}
	e.agents.setDeleteErr("leftover", nil)
	ctrl.finish <- StatusCompleted
	svcEventually(t, "the leftover agent's deletion", func() bool {
		return slices.Contains(e.agents.deletedIDs(), "leftover")
	})
}

// A folder with forum.json but neither a snapshot nor a draft (a launch
// that died before drafts existed) is removed quietly, and a folder with
// neither forum.json nor draft.json (a draft whose creation died) is
// removed too; neither is reported.
func TestSvcRecoverRemovesIncompleteFolders(t *testing.T) {
	e := svcSetup(t)
	base := e.scope.BaseDirectory
	launched := uuid.NewString()
	s, err := CreateStore(base, launched)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.WriteConfig([]byte(svcSimpleJSON)); err != nil {
		t.Fatal(err)
	}
	if err = setAgentsMarker(s, []string{"leftover"}); err != nil {
		t.Fatal(err)
	}
	empty := uuid.NewString()
	if _, err = CreateStore(base, empty); err != nil {
		t.Fatal(err)
	}
	busy := uuid.NewString()
	if _, err = CreateStore(base, busy); err != nil {
		t.Fatal(err)
	}
	guard := &Store{base: base, id: busy, root: filepath.Join(base, busy)}
	if err = guard.Lock(); err != nil {
		t.Fatal(err)
	}
	defer guard.Unlock()
	if err = e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if calls := e.stuck.list(); len(calls) != 0 {
		t.Errorf("reported %q", calls)
	}
	for _, id := range []string{launched, empty} {
		if _, err := os.Stat(filepath.Join(base, id)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("folder %s still there: %v", id, err)
		}
	}
	if !slices.Equal(e.agents.deletedIDs(), []string{"leftover"}) {
		t.Errorf("deleted %v, want the abandoned launch's agent", e.agents.deletedIDs())
	}
	if _, err := os.Stat(filepath.Join(base, busy)); err != nil {
		t.Errorf("a draft being created was removed: %v", err)
	}
}
