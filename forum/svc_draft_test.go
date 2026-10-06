// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"errors"
	"slices"
	"strings"
	"testing"
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
