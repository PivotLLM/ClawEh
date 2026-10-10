// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

var errSvcHost = errors.New("host refused")

func TestSvcLaunchWritesTheForum(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch("")
	if !validForumID(id) {
		t.Fatalf("id %q is not a UUID", id)
	}
	e.running(id)
	s := e.store(id)

	parts, err := s.ReadParticipants()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parts.Participants["spare"]; ok {
		t.Error("a participant of a disabled layer was recorded")
	}
	alice := parts.Participants["alice"]
	if alice.Form != FormExisting || alice.AgentID != "alice" || alice.Created || alice.Name != "alice" {
		t.Errorf("alice = %+v", alice)
	}
	bob := parts.Participants["bob"]
	if bob.Form != FormClone || !bob.Created || bob.Model != "large" || bob.Name != "Bob" || !validForumID(bob.AgentID) {
		t.Errorf("bob = %+v", bob)
	}
	editor := parts.Participants["editor"]
	if editor.Form != FormFresh || !editor.Created || editor.Model != "default" || editor.Mode != FreshModeContext {
		t.Errorf("editor = %+v", editor)
	}
	if len(e.agents.clones) != 1 || e.agents.clones[0] != (CloneSpec{Source: "bob", Model: "large", Owner: "launcher"}) {
		t.Errorf("clones = %+v", e.agents.clones)
	}
	wantFresh := FreshSpec{Model: "default", SystemPrompt: "Be brief.", Mode: FreshModeContext, Owner: "launcher"}
	if len(e.agents.freshes) != 1 || e.agents.freshes[0] != wantFresh {
		t.Errorf("freshes = %+v", e.agents.freshes)
	}

	marker, ok := e.marker(id, cleanupAgents)
	if !ok {
		t.Fatal("no agents marker")
	}
	var pending []string
	if jsonErr := json.Unmarshal([]byte(marker), &pending); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	slices.Sort(pending)
	want := []string{bob.AgentID, editor.AgentID}
	slices.Sort(want)
	if !slices.Equal(pending, want) {
		t.Errorf("agents marker = %v, want %v", pending, want)
	}
	if _, ok := e.marker(id, cleanupNotice); !ok {
		t.Error("no notice marker")
	}

	snap, err := s.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.ForumID != id || snap.Name != "svc-test" || snap.BaseDirectory != e.scope.BaseDirectory {
		t.Errorf("snapshot identity = %+v", snap)
	}
	if snap.Origin != (Origin{AgentID: "launcher", Channel: "test", ChatID: "chat-1"}) {
		t.Errorf("origin = %+v", snap.Origin)
	}
	if got := snap.Deadline.Sub(snap.LaunchedAt); got != 600*time.Second {
		t.Errorf("deadline is %v after launch, want 600s", got)
	}
	if !slices.Equal(snap.Layers, []string{"talk"}) || !slices.Equal(snap.ResultLayers, []string{"talk"}) {
		t.Errorf("layers %v, result layers %v", snap.Layers, snap.ResultLayers)
	}
	if snap.Models["bob"] != "large" || snap.Models["editor"] != "default" {
		t.Errorf("models = %v", snap.Models)
	}
	if _, ok := snap.ModeratorSchemas["talk"]; !ok {
		t.Error("no moderator schema for layer talk")
	}
	if snap.Seed < 0 {
		t.Errorf("seed %d is negative", snap.Seed)
	}
	for srcID, want := range map[string]string{"note": "A short note.", "data": "{\n  \"k\": 1\n}", "doc": "# Doc\n\nBody.\n"} {
		rec, ok := snap.Sources[srcID]
		if !ok {
			t.Errorf("source %s not materialised", srcID)
			continue
		}
		data, readErr := s.ReadFile(rec.File)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(data) != want || rec.Digest != digest(data) {
			t.Errorf("source %s = %q (digest %s)", srcID, data, rec.Digest)
		}
	}
	raw, err := s.ReadConfig()
	wantRaw, fmtErr := formatConfig([]byte(svcConfigJSON))
	if err != nil || fmtErr != nil || string(raw) != string(wantRaw) {
		t.Errorf("the run's forum.json is not the forum's configuration (%v, %v)", err, fmtErr)
	}
	if snap.Run != 1 || s.RunNumber() != 1 || filepath.Base(s.Root()) != "1" {
		t.Errorf("first launch is run %d in %s, want run 1", snap.Run, s.Root())
	}
	commits, err := s.ReadCommits()
	if err != nil || len(commits) != 1 || commits[0].Kind != CommitLaunched {
		t.Errorf("commits = %+v (%v)", commits, err)
	}
}

func TestSvcLaunchUsesTheConfiguredSeed(t *testing.T) {
	e := svcSetup(t)
	cfg := strings.Replace(svcSimpleJSON, `"version": 1,`, `"version": 1, "seed": 42,`, 1)
	id, _ := e.launch(cfg)
	snap, err := e.store(id).ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Seed != 42 {
		t.Errorf("seed = %d, want 42", snap.Seed)
	}
}

// A launch that fails leaves the forum as it was: no run, no markers, every
// temporary agent deleted; it can then be launched again.
func TestSvcLaunchFailuresLeaveTheForum(t *testing.T) {
	tests := []struct {
		name    string
		cfg     string
		setup   func(e *svcEnv)
		wantErr func(error) bool
	}{
		{
			name:    "invalid configuration",
			cfg:     `{"version": 1}`,
			wantErr: func(err error) bool { return errors.As(err, new(*ValidationError)) },
		},
		{
			name: "fresh participant cannot be created after a clone was",
			cfg:  svcConfigJSON,
			setup: func(e *svcEnv) {
				e.agents.createErr["fresh:default"] = errSvcHost
			},
			wantErr: func(err error) bool {
				return errors.Is(err, errSvcHost) && strings.Contains(err.Error(), `participant "editor"`)
			},
		},
		{
			name: "clone cannot be created",
			cfg:  svcConfigJSON,
			setup: func(e *svcEnv) {
				e.agents.createErr["clone:bob"] = errSvcHost
			},
			wantErr: func(err error) bool {
				return errors.Is(err, errSvcHost) && strings.Contains(err.Error(), `could not clone agent "bob"`)
			},
		},
		{
			name: "controller cannot open",
			cfg:  svcConfigJSON,
			setup: func(e *svcEnv) {
				e.ctrls.openErr = ErrCorrupt
			},
			wantErr: func(err error) bool {
				return errors.Is(err, ErrCorrupt) && strings.Contains(err.Error(), "could not start")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := svcSetup(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			id, err := svcLaunch(t, e.svc, tt.cfg, e.opts())
			if err == nil || !tt.wantErr(err) {
				t.Fatalf("Launch = %v", err)
			}
			if ids := e.forumIDs(); !slices.Equal(ids, []string{id}) {
				t.Errorf("forums = %v, want only %s", ids, id)
			}
			var left []string
			err = filepath.WalkDir(filepath.Join(e.scope.BaseDirectory, id), func(p string, d os.DirEntry, err error) error {
				if err == nil && p != filepath.Join(e.scope.BaseDirectory, id) {
					left = append(left, d.Name())
				}
				return err
			})
			slices.Sort(left)
			if err != nil || !slices.Equal(left, []string{fileMeta, fileConfig, dirRuns}) {
				t.Errorf("entries left in the forum = %v (%v), want only %s, %s and an empty %s/", left, err, fileMeta, fileConfig, dirRuns)
			}
			created, deleted := e.agents.createdIDs(), e.agents.deletedIDs()
			slices.Sort(created)
			slices.Sort(deleted)
			if !slices.Equal(created, deleted) {
				t.Errorf("created %v but deleted %v", created, deleted)
			}
			entries, err := os.ReadDir(filepath.Join(e.scope.BaseDirectory, dirCleanup))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("cleanup entries left behind: %v", entries)
			}
			if tt.setup == nil {
				return
			}
			clear(e.agents.createErr)
			e.ctrls.openErr = nil
			if n, err := e.svc.Launch(t.Context(), id, e.opts()); err != nil || n != 1 {
				t.Fatalf("launch once the failure is gone = run %d, %v; want run 1", n, err)
			}
			e.ctrls.get(t, id)
			if e.store(id).RunNumber() != 1 {
				t.Error("no run 1 after the launch")
			}
		})
	}
}

func TestSvcValidate(t *testing.T) {
	e := svcSetup(t)
	if err := e.svc.Validate(t.Context(), []byte(svcConfigJSON), e.opts()); err != nil {
		t.Fatalf("valid configuration: %v", err)
	}
	if len(e.agents.createdIDs()) != 0 || len(e.forumIDs()) != 0 {
		t.Error("Validate created something")
	}
	bad := strings.Replace(svcConfigJSON, `"model": "large"`, `"model": "huge"`, 1)
	err := e.svc.Validate(t.Context(), []byte(bad), e.opts())
	if ve, ok := errors.AsType[*ValidationError](err); !ok || !strings.Contains(ve.Error(), "participants.bob.model") {
		t.Errorf("unknown model: %v", err)
	}
}

func TestSvcCeilings(t *testing.T) {
	e := svcSetup(t)
	var current atomic.Int64
	current.Store(5)
	capped := New(Host{Messenger: svcMessenger{}, Agents: e.agents, Notifier: e.notifier, Logger: e.logger},
		WithCeilings(func() Ceilings { return Ceilings{MaxCalls: int(current.Load())} }))
	t.Cleanup(func() { svcClose(t, capped) })
	err := capped.Validate(t.Context(), []byte(svcSimpleJSON), e.opts())
	if ve, ok := errors.AsType[*ValidationError](err); !ok || !strings.Contains(ve.Error(), "limits.max_calls: 10 is more than this install allows (5)") {
		t.Errorf("Validate above the ceiling = %v", err)
	}
	if id, err := svcLaunch(t, capped, svcSimpleJSON, e.opts()); err == nil || e.store(id).RunNumber() != 0 {
		t.Errorf("Launch above the ceiling = %v", err)
	}
	// The ceiling is read at every check: raising it to the configured
	// value admits the same configuration.
	current.Store(10)
	if err := capped.Validate(t.Context(), []byte(svcSimpleJSON), e.opts()); err != nil {
		t.Errorf("Validate at the ceiling = %v", err)
	}
	if got := capped.Ceilings(); got.MaxCalls != 10 {
		t.Errorf("Ceilings() = %+v, want MaxCalls 10", got)
	}
	if got := e.svc.Ceilings(); got != (Ceilings{}) {
		t.Errorf("Ceilings() without WithCeilings = %+v, want none", got)
	}
}

func TestSvcModels(t *testing.T) {
	e := svcSetup(t)
	got, err := e.svc.Models(t.Context(), "launcher")
	if err != nil || len(got) != 2 || got[0].Name != "default" {
		t.Errorf("Models = %v, %v", got, err)
	}
}

func TestSvcStatusAndList(t *testing.T) {
	e := svcSetup(t)
	first, _ := e.launch("")
	// The second launch must be later than the first for the list order.
	firstAt := e.summary(first).LaunchedAt
	for !time.Now().UTC().After(firstAt) {
		runtime.Gosched()
	}
	second, _ := e.launch(svcSimpleJSON)

	sum, err := e.svc.Status(t.Context(), e.scope, first, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum.ForumID != first || sum.Name != "svc-test" || sum.Status != StatusRunning || sum.MaxCalls != 20 {
		t.Errorf("summary = %+v", sum)
	}
	if len(sum.Layers) != 2 || sum.Layers[0].LayerID != "talk" || !sum.Layers[0].Enabled ||
		sum.Layers[0].MaxRounds != 2 || sum.Layers[1].Enabled {
		t.Errorf("layers = %+v", sum.Layers)
	}

	list, err := e.svc.List(t.Context(), e.scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ForumID != second || list[1].ForumID != first {
		t.Errorf("list is not newest first: %+v", list)
	}

	// Another agent's scope sees none of them, live or not.
	other := Scope{AgentID: "other", BaseDirectory: filepath.Join(t.TempDir(), "forums")}
	if list, err := e.svc.List(t.Context(), other); err != nil || len(list) != 0 {
		t.Errorf("other scope list = %v, %v", list, err)
	}
	if _, err := e.svc.Status(t.Context(), other, first, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("other scope status = %v", err)
	}
	if err := e.svc.Pause(t.Context(), other, first); !errors.Is(err, ErrNotFound) {
		t.Errorf("other scope pause = %v", err)
	}
	for _, id := range []string{uuid.NewString(), "not-a-uuid", "../x"} {
		if _, err := e.svc.Status(t.Context(), e.scope, id, 0); !errors.Is(err, ErrNotFound) {
			t.Errorf("status %q = %v", id, err)
		}
	}

	// A paused (not live) forum is read from disk.
	if err := e.svc.Pause(t.Context(), e.scope, first); err != nil {
		t.Fatal(err)
	}
	e.settled(first, StatusPaused)
}

func TestSvcListSkipsUnreadableForums(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch(svcSimpleJSON)
	e.restart()
	if err := os.WriteFile(e.store(id).Path(fileSnapshot), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.List(t.Context(), e.scope)
	if err != nil || len(list) != 0 {
		t.Errorf("List = %v, %v", list, err)
	}
	if !e.logger.has("forum " + e.ref(id) + ": status") {
		t.Error("the unreadable forum was not logged")
	}
}

func TestSvcPauseResumeCancelLifecycle(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch("")
	e.running(id)

	for range 2 {
		if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
			t.Fatalf("pause: %v", err)
		}
	}
	e.settled(id, StatusPaused)
	if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
		t.Errorf("pause of a paused forum: %v", err)
	}

	for range 2 {
		if err := e.svc.Resume(t.Context(), e.scope, id); err != nil {
			t.Fatalf("resume: %v", err)
		}
	}
	e.running(id)
	if n := e.ctrls.openCount(); n != 2 {
		t.Errorf("controller opened %d times, want 2 (launch and one resume)", n)
	}

	for range 2 {
		if err := e.svc.Cancel(t.Context(), e.scope, id); err != nil {
			t.Fatalf("cancel: %v", err)
		}
	}
	e.settled(id, StatusCancelled)
	if err := e.svc.Cancel(t.Context(), e.scope, id); err != nil {
		t.Errorf("cancel of a cancelled forum: %v", err)
	}
	if err := e.svc.Pause(t.Context(), e.scope, id); !errors.Is(err, ErrInvalidState) || !strings.Contains(err.Error(), "is cancelled and cannot be paused") {
		t.Errorf("pause after cancel: %v", err)
	}
	if err := e.svc.Resume(t.Context(), e.scope, id); !errors.Is(err, ErrInvalidState) || !strings.Contains(err.Error(), "cannot be resumed") {
		t.Errorf("resume after cancel: %v", err)
	}
	if e.notifier.count() != 1 {
		t.Errorf("%d notices, want 1", e.notifier.count())
	}
}

func TestSvcCompletedForumRefusesControl(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	c.finish <- StatusCompleted
	e.settled(id, StatusCompleted)
	if err := e.svc.Cancel(t.Context(), e.scope, id); !errors.Is(err, ErrInvalidState) || !strings.Contains(err.Error(), "already completed") {
		t.Errorf("cancel: %v", err)
	}
	if err := e.svc.Pause(t.Context(), e.scope, id); !errors.Is(err, ErrInvalidState) {
		t.Errorf("pause: %v", err)
	}
	if err := e.svc.Resume(t.Context(), e.scope, id); !errors.Is(err, ErrInvalidState) {
		t.Errorf("resume: %v", err)
	}
}

func TestSvcCancelPausedForum(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch("")
	if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusPaused)
	if err := e.svc.Cancel(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusCancelled)
	if e.keptAlive(id) {
		t.Error("a cancelled forum is still kept alive")
	}
	res, err := e.svc.Results(t.Context(), e.scope, id, 0)
	if err != nil || res.Status != StatusCancelled || res.Complete {
		t.Errorf("results = %+v, %v", res, err)
	}
	if len(e.agents.deletedIDs()) != 2 {
		t.Errorf("deleted %v, want both temporary agents", e.agents.deletedIDs())
	}
}

func TestSvcCancellationDominates(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch("")
	e.restart() // running on disk, no live controller
	e.appendCommits(id, Commit{Kind: CommitCancelRequested})

	if err := e.svc.Pause(t.Context(), e.scope, id); !errors.Is(err, ErrInvalidState) || !strings.Contains(err.Error(), "being cancelled") {
		t.Errorf("pause of a cancelling forum: %v", err)
	}
	if err := e.svc.Resume(t.Context(), e.scope, id); !errors.Is(err, ErrInvalidState) || !strings.Contains(err.Error(), "being cancelled") {
		t.Errorf("resume of a cancelling forum: %v", err)
	}
	if e.status(id) != StatusCancelling {
		t.Fatalf("status = %s, want cancelling", e.status(id))
	}
	if err := e.svc.Cancel(t.Context(), e.scope, id); err != nil {
		t.Fatalf("cancel of a cancelling forum: %v", err)
	}
	e.settled(id, StatusCancelled)
}

func TestSvcLiveCancelWinsOverPendingPause(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	e.running(id)
	// A pause is pending (the fake does not act on it until woken).
	if err := c.commit(&Commit{Kind: CommitPauseRequested}); err != nil {
		t.Fatal(err)
	}
	c.pause.Store(true)
	if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
		t.Errorf("pause while pausing: %v", err)
	}
	if err := e.svc.Resume(t.Context(), e.scope, id); !errors.Is(err, ErrInvalidState) || !strings.Contains(err.Error(), "still pausing") {
		t.Errorf("resume while pausing: %v", err)
	}
	if err := e.svc.Cancel(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusCancelled)
}

// While a run is pausing, a configuration change and a launch are refused
// as such, not as "running": the forum is about to be paused.
func TestSvcEditAndLaunchWhilePausing(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	e.running(id)
	if err := c.commit(&Commit{Kind: CommitPauseRequested}); err != nil {
		t.Fatal(err)
	}
	c.pause.Store(true)
	want := "forum svc-test (" + id + ") is still pausing; try again once it is paused"
	for name, op := range map[string]func() error{
		"update": func() error { return e.svc.UpdateConfig(t.Context(), e.scope, id, []byte(`{"name": "x"}`)) },
		"launch": func() error { _, err := e.svc.Launch(t.Context(), id, e.opts()); return err },
	} {
		if err := op(); !errors.Is(err, ErrInvalidState) || err.Error() != want {
			t.Errorf("%s while pausing = %v, want %q", name, err, want)
		}
	}
	if err := e.svc.Cancel(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusCancelled)
}

// While a run is being cancelled, a configuration change, a launch and a
// delete say so.
func TestSvcEditLaunchDeleteWhileCancelling(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch("")
	e.restart() // running on disk, no live controller
	e.appendCommits(id, Commit{Kind: CommitCancelRequested})
	want := "forum svc-test (" + id + ") is being cancelled"
	for name, op := range map[string]func() error{
		"update": func() error { return e.svc.UpdateConfig(t.Context(), e.scope, id, []byte(`{"name": "x"}`)) },
		"launch": func() error { _, err := e.svc.Launch(t.Context(), id, e.opts()); return err },
		"delete": func() error { return e.svc.Delete(t.Context(), e.scope, id) },
	} {
		if err := op(); !errors.Is(err, ErrInvalidState) || err.Error() != want {
			t.Errorf("%s while cancelling = %v, want %q", name, err, want)
		}
	}
	if err := e.svc.Cancel(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusCancelled)
}

func TestSvcControlOfInterruptedForums(t *testing.T) {
	t.Run("pause an interrupted running forum", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		e.restart()
		if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
			t.Fatal(err)
		}
		e.settled(id, StatusPaused)
	})
	t.Run("pause completes an interrupted pause", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		e.restart()
		e.appendCommits(id, Commit{Kind: CommitPauseRequested})
		if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
			t.Fatal(err)
		}
		e.settled(id, StatusPaused)
	})
	t.Run("resume an interrupted running forum", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		e.restart()
		if err := e.svc.Resume(t.Context(), e.scope, id); err != nil {
			t.Fatal(err)
		}
		e.running(id)
	})
	t.Run("resume clears an interrupted pause", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		e.restart()
		e.appendCommits(id, Commit{Kind: CommitPauseRequested})
		if err := e.svc.Resume(t.Context(), e.scope, id); err != nil {
			t.Fatal(err)
		}
		e.running(id)
	})
	t.Run("cancel an interrupted running forum", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		e.restart()
		if err := e.svc.Cancel(t.Context(), e.scope, id); err != nil {
			t.Fatal(err)
		}
		e.settled(id, StatusCancelled)
		if e.notifier.count() != 1 {
			t.Errorf("%d notices, want 1", e.notifier.count())
		}
	})
}

func TestSvcControlOfLockedForum(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch("")
	if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusPaused)
	other := e.store(id)
	if err := other.Lock(); err != nil {
		t.Fatal(err)
	}
	defer other.Unlock()
	for name, op := range map[string]func() error{
		"pause":  func() error { return e.svc.Pause(t.Context(), e.scope, id) },
		"resume": func() error { return e.svc.Resume(t.Context(), e.scope, id) },
		"cancel": func() error { return e.svc.Cancel(t.Context(), e.scope, id) },
		"delete": func() error { return e.svc.Delete(t.Context(), e.scope, id) },
	} {
		if err := op(); !errors.Is(err, ErrLocked) {
			t.Errorf("%s: %v, want ErrLocked", name, err)
		}
	}
}

func TestSvcControlOfUnknownForum(t *testing.T) {
	e := svcSetup(t)
	id := uuid.NewString()
	for name, op := range map[string]func() error{
		"pause":  func() error { return e.svc.Pause(t.Context(), e.scope, id) },
		"resume": func() error { return e.svc.Resume(t.Context(), e.scope, id) },
		"cancel": func() error { return e.svc.Cancel(t.Context(), e.scope, id) },
		"results": func() error {
			_, err := e.svc.Results(t.Context(), e.scope, id, 0)
			return err
		},
	} {
		if err := op(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
}

func TestSvcResults(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	e.running(id)
	res, err := e.svc.Results(t.Context(), e.scope, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Complete || res.Status != StatusRunning || res.Transcript != fileTranscript ||
		!slices.Equal(res.Omissions, []string{"layer talk did not run"}) {
		t.Errorf("partial result = %+v", res)
	}
	if _, readErr := e.store(id).ReadResult(); !errors.Is(readErr, ErrNotFound) {
		t.Errorf("result.json exists while running: %v", readErr)
	}

	c.finish <- StatusCompleted
	e.settled(id, StatusCompleted)
	res, err = e.svc.Results(t.Context(), e.scope, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := e.store(id).ReadResult()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Complete || res.Status != StatusCompleted || res.EndedAt.IsZero() || !res.EndedAt.Equal(onDisk.EndedAt) {
		t.Errorf("terminal result = %+v", res)
	}
}

// The service builds partial results with the controller's buildResult:
// omissions name what is missing, and an after_round round that is not
// published stays hidden.
func TestSvcResultOmissions(t *testing.T) {
	cfg, err := decodeConfig([]byte(strings.Replace(svcSimpleJSON, `"max_rounds": 1`, `"max_rounds": 2`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	snap := &Snapshot{ForumID: "f", Layers: []string{"talk"}, ResultLayers: []string{"talk"}}
	out := func(pid string, round int) OutputRecord {
		return OutputRecord{OutputID: pid + strconv.Itoa(round), LayerID: "talk", Round: round, ParticipantID: pid, Turn: turnID(round, pid)}
	}
	ended := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	st := &State{
		Status: StatusIncomplete, Reason: EndDeadline, Calls: 3, UpdatedAt: ended,
		Layers: map[string]*LayerState{"talk": {Started: true, Round: 1, Calls: 3, Outputs: []OutputRecord{out("alice", 1)}}},
	}
	res := buildResult(cfg, snap, st)
	if res.Complete || !res.EndedAt.Equal(ended) || res.Calls != 3 || res.Reason != EndDeadline {
		t.Errorf("result = %+v", res)
	}
	want := []string{"layer talk did not end", "layer talk round 1: no output from bob", "layer talk round 1: 1 committed output(s) not published (round incomplete)"}
	if !slices.Equal(res.Omissions, want) {
		t.Errorf("omissions = %q, want %q", res.Omissions, want)
	}
	if len(res.Layers) != 1 || len(res.Layers[0].Outputs) != 0 {
		t.Errorf("an unpublished round is in the result: %+v", res.Layers)
	}

	// Round 1 published, round 2 partly committed: only round 1 shows.
	ls := st.Layers["talk"]
	ls.Outputs = []OutputRecord{out("alice", 1), out("bob", 1), out("bob", 2)}
	ls.RoundsPublished, ls.Round = 1, 2
	st.Status, st.Reason = StatusRunning, ""
	res = buildResult(cfg, snap, st)
	if got := res.Layers[0].Outputs; len(got) != 2 || got[0].OutputID != "alice1" || got[1].OutputID != "bob1" {
		t.Errorf("outputs = %+v, want round 1 in participant order", got)
	}
	want = []string{"layer talk did not end", "layer talk round 2: no output from alice", "layer talk round 2: 1 committed output(s) not published (round incomplete)"}
	if !slices.Equal(res.Omissions, want) || !res.EndedAt.IsZero() {
		t.Errorf("omissions = %q, want %q (ended %v)", res.Omissions, want, res.EndedAt)
	}

	ls.Outputs = append(ls.Outputs, out("alice", 2))
	ls.RoundsPublished, ls.Ended, ls.EndReason = 2, true, EndRoundLimit
	st.Status, st.Reason = StatusCompleted, EndCompleted
	res = buildResult(cfg, snap, st)
	if !res.Complete || len(res.Omissions) != 0 || res.Layers[0].EndReason != EndRoundLimit || len(res.Layers[0].Outputs) != 4 {
		t.Errorf("complete result = %+v", res)
	}
}

func TestSvcDelete(t *testing.T) {
	t.Run("running forum is refused", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		e.running(id)
		err := e.svc.Delete(t.Context(), e.scope, id)
		if !errors.Is(err, ErrInvalidState) || !strings.Contains(err.Error(), "is running; pause or cancel it first") {
			t.Errorf("delete running: %v", err)
		}
	})
	t.Run("interrupted running forum is refused", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		e.restart()
		if err := e.svc.Delete(t.Context(), e.scope, id); !errors.Is(err, ErrInvalidState) {
			t.Errorf("delete interrupted: %v", err)
		}
		if len(e.forumIDs()) != 1 {
			t.Error("the forum was removed")
		}
	})
	t.Run("paused forum is deleted with its agents", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
			t.Fatal(err)
		}
		e.settled(id, StatusPaused)
		if err := e.svc.Delete(t.Context(), e.scope, id); err != nil {
			t.Fatal(err)
		}
		if len(e.forumIDs()) != 0 {
			t.Error("the forum is still listed")
		}
		if len(e.agents.deletedIDs()) != 2 {
			t.Errorf("deleted %v, want both temporary agents", e.agents.deletedIDs())
		}
		if e.keptAlive(id) {
			t.Error("a deleted forum is still kept alive")
		}
		if e.notifier.count() != 0 {
			t.Error("deleting a paused forum sent a completion notice")
		}
		if err := e.svc.Delete(t.Context(), e.scope, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("second delete: %v, want ErrNotFound", err)
		}
	})
	t.Run("terminal forum is deleted", func(t *testing.T) {
		e := svcSetup(t)
		id, c := e.launch("")
		c.finish <- StatusFailed
		e.settled(id, StatusFailed)
		if err := e.svc.Delete(t.Context(), e.scope, id); err != nil {
			t.Fatal(err)
		}
		if len(e.forumIDs()) != 0 {
			t.Error("the forum is still listed")
		}
	})
	t.Run("agent that cannot be deleted keeps the forum", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
			t.Fatal(err)
		}
		e.settled(id, StatusPaused)
		parts, err := e.store(id).ReadParticipants()
		if err != nil {
			t.Fatal(err)
		}
		stuck := parts.Participants["bob"].AgentID
		gone := parts.Participants["editor"].AgentID
		e.agents.setDeleteErr(stuck, errSvcHost)
		e.agents.setDeleteErr(gone, ErrNotFound)
		err = e.svc.Delete(t.Context(), e.scope, id)
		if !errors.Is(err, errSvcHost) || !strings.Contains(err.Error(), id) || !strings.Contains(err.Error(), stuck) {
			t.Fatalf("delete: %v", err)
		}
		if len(e.forumIDs()) != 1 {
			t.Fatal("the forum was removed")
		}
		if m, _ := e.marker(id, cleanupAgents); m != `["`+stuck+`"]` {
			t.Errorf("agents marker = %s, want only %s (an ErrNotFound delete counts as done)", m, stuck)
		}
		e.agents.setDeleteErr(stuck, nil)
		if err := e.svc.Delete(t.Context(), e.scope, id); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if len(e.forumIDs()) != 0 {
			t.Error("the forum is still listed after the retry")
		}
	})
	t.Run("corrupt forum can be deleted", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		e.restart()
		if err := os.WriteFile(e.store(id).Path(fileConfig), []byte(`{"tampered": true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Delete(t.Context(), e.scope, id); err != nil {
			t.Fatal(err)
		}
		if len(e.forumIDs()) != 0 {
			t.Error("the forum is still listed")
		}
	})
	t.Run("absent and malformed IDs", func(t *testing.T) {
		e := svcSetup(t)
		if err := e.svc.Delete(t.Context(), e.scope, uuid.NewString()); !errors.Is(err, ErrNotFound) {
			t.Errorf("absent forum: %v, want ErrNotFound", err)
		}
		if err := e.svc.Delete(t.Context(), e.scope, "../../etc"); !errors.Is(err, ErrNotFound) {
			t.Errorf("malformed id: %v", err)
		}
	})
}

func TestSvcCompletionNotice(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	c.finish <- StatusCompleted
	e.settled(id, StatusCompleted)
	svcEventually(t, "notice", func() bool { return e.notifier.count() == 1 })
	if p := e.notifier.problems(); len(p) != 0 {
		t.Errorf("notice order: %v", p)
	}
	res, origin := e.notifier.last()
	if res.ForumID != id || res.Status != StatusCompleted || !res.Complete {
		t.Errorf("notice result = %+v", res)
	}
	if origin != (Origin{AgentID: "launcher", Channel: "test", ChatID: "chat-1"}) {
		t.Errorf("notice origin = %+v", origin)
	}
	if chat := e.notifier.lastChat(); chat != (Chat{Channel: "test", ChatID: "chat-1"}) {
		t.Errorf("notice launching chat = %+v, want the launch call's", chat)
	}
	if _, ok := e.marker(id, cleanupNotice); ok {
		t.Error("notice marker left behind")
	}
	if _, ok := e.marker(id, cleanupAgents); ok {
		t.Error("agents marker left behind")
	}
	if len(e.agents.deletedIDs()) != 2 {
		t.Errorf("deleted %v", e.agents.deletedIDs())
	}

	// A restart does not notify or delete again.
	e.restart()
	if err := e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
		t.Fatal(err)
	}
	if e.notifier.count() != 1 || len(e.agents.deletedIDs()) != 2 {
		t.Errorf("after restart: %d notices, deleted %v", e.notifier.count(), e.agents.deletedIDs())
	}
}

// The launching chat is known only to the process that saw the launch: a
// run that ends after a restart is notified without it, whatever the
// forum's directory records.
func TestSvcNoticeLaunchChatAfterRestart(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch("")
	e.restart() // running on disk, no live controller
	e.appendCommits(id, Commit{Kind: CommitCancelRequested})
	if err := e.svc.Cancel(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusCancelled)
	svcEventually(t, "notice", func() bool { return e.notifier.count() == 1 })
	if chat := e.notifier.lastChat(); chat != (Chat{}) {
		t.Errorf("launching chat after a restart = %+v, want none", chat)
	}
	if _, origin := e.notifier.last(); origin.ChatID != "chat-1" {
		t.Errorf("recorded origin = %+v", origin)
	}
}

// A notice that fails keeps its marker and is retried by the keep-alive
// loop until it is delivered.
func TestSvcNotifyFailureIsRetried(t *testing.T) {
	e := svcSetup(t)
	e.notifier.failures = 2
	id, c := e.launch("")
	c.finish <- StatusCompleted
	e.settled(id, StatusCompleted)
	for try := 1; try <= 2; try++ {
		svcEventually(t, fmt.Sprintf("failed try %d recorded", try), func() bool {
			return e.noticeTries(id) == try && !e.notifying(id)
		})
		if _, ok := e.marker(id, cleanupNotice); !ok {
			t.Fatalf("after failed try %d the notice is no longer pending", try)
		}
		e.svc.keepAliveTick(t.Context())
	}
	svcEventually(t, "notice delivered", func() bool {
		_, pending := e.marker(id, cleanupNotice)
		return !pending && e.noticeTries(id) == 0 && !e.notifying(id)
	})
	if n := e.notifier.count(); n != 3 {
		t.Errorf("notifier called %d times, want 3", n)
	}
	// Delivered: a further tick sends nothing.
	e.svc.keepAliveTick(t.Context())
	if n := e.notifier.count(); n != 3 {
		t.Errorf("notifier called %d times after delivery, want 3", n)
	}
}

// A notice that keeps failing is given up after maxNoticeTries tries, and
// its marker cleared, so it is not retried forever.
func TestSvcNotifyGivenUp(t *testing.T) {
	e := svcSetup(t)
	e.notifier.err = errSvcHost
	id, c := e.launch("")
	c.finish <- StatusCompleted
	e.settled(id, StatusCompleted)
	for try := 1; try < maxNoticeTries; try++ {
		svcEventually(t, fmt.Sprintf("failed try %d recorded", try), func() bool {
			return e.noticeTries(id) == try && !e.notifying(id)
		})
		e.svc.keepAliveTick(t.Context())
	}
	svcEventually(t, "notice given up", func() bool {
		_, pending := e.marker(id, cleanupNotice)
		return !pending && e.noticeTries(id) == 0 && !e.notifying(id)
	})
	if n := e.notifier.count(); n != maxNoticeTries {
		t.Errorf("notifier called %d times, want %d", n, maxNoticeTries)
	}
	e.svc.keepAliveTick(t.Context())
	if n := e.notifier.count(); n != maxNoticeTries {
		t.Errorf("notifier called %d times after giving up, want %d", n, maxNoticeTries)
	}
}

// A result.json that cannot be written holds the notice back; the
// keep-alive loop writes it and notifies once it can.
func TestSvcNoNoticeWithoutResult(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	// The controller commits the end but cannot write result.json.
	resultPath := e.store(id).Path(fileResult)
	if err := os.WriteFile(resultPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(resultPath, 0); err != nil {
		t.Fatal(err)
	}
	c.finish <- StatusCompleted
	e.settled(id, StatusCompleted)
	if e.notifier.count() != 0 {
		t.Error("notified without a readable result.json")
	}
	if _, ok := e.marker(id, cleanupNotice); !ok {
		t.Error("the notice is no longer pending")
	}
	if e.noticeTries(id) != 1 {
		t.Errorf("notice tries = %d, want 1", e.noticeTries(id))
	}
	if len(e.agents.deletedIDs()) != 2 {
		t.Errorf("temporary agents not deleted: %v", e.agents.deletedIDs())
	}
	if err := os.Remove(resultPath); err != nil {
		t.Fatal(err)
	}
	e.svc.keepAliveTick(t.Context())
	svcEventually(t, "notice delivered", func() bool {
		_, pending := e.marker(id, cleanupNotice)
		return !pending && e.notifier.count() == 1
	})
	if res, err := e.store(id).ReadResult(); err != nil || res.Status != StatusCompleted {
		t.Errorf("result.json = %+v, %v", res, err)
	}
}

func TestSvcRecover(t *testing.T) {
	t.Run("running and queued forums resume", func(t *testing.T) {
		e := svcSetup(t)
		running, _ := e.launch("")
		queued, _ := e.launch(svcSimpleJSON)
		e.restart()
		// queued: the launch died between the snapshot and CommitLaunched.
		s := e.store(queued)
		for _, f := range []string{commitRel(1), fileState} {
			if err := os.Remove(s.Path(f)); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
		if e.status(queued) != StatusQueued {
			t.Fatalf("status = %s, want queued", e.status(queued))
		}
		if err := e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
			t.Fatal(err)
		}
		e.running(running)
		e.running(queued)
		commits, err := e.store(queued).ReadCommits()
		if err != nil || len(commits) != 1 || commits[0].Kind != CommitLaunched {
			t.Errorf("queued commits = %+v (%v)", commits, err)
		}
	})
	t.Run("paused forum stays paused and is kept alive", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		e.restart()
		e.appendCommits(id, Commit{Kind: CommitPauseRequested}, Commit{Kind: CommitPaused})
		opens := e.ctrls.openCount()
		if err := e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
			t.Fatal(err)
		}
		if e.ctrls.openCount() != opens {
			t.Error("a paused forum was opened")
		}
		e.settled(id, StatusPaused)
		for _, agentID := range e.agents.createdIDs() {
			if e.agents.touches(agentID) != 1 {
				t.Errorf("agent %s touched %d times at recovery, want 1", agentID, e.agents.touches(agentID))
			}
		}
		if !e.keptAlive(id) {
			t.Error("not registered for keep-alive")
		}
	})
	t.Run("pending pause and cancel are honoured", func(t *testing.T) {
		e := svcSetup(t)
		pausing, _ := e.launch("")
		cancelling, _ := e.launch(svcSimpleJSON)
		e.restart()
		e.appendCommits(pausing, Commit{Kind: CommitPauseRequested})
		e.appendCommits(cancelling, Commit{Kind: CommitCancelRequested})
		if err := e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
			t.Fatal(err)
		}
		e.settled(pausing, StatusPaused)
		e.settled(cancelling, StatusCancelled)
		svcEventually(t, "notice", func() bool { return e.notifier.count() == 1 })
	})
	t.Run("terminal forum finishes its cleanup and notice", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		e.restart()
		e.appendCommits(id, Commit{Kind: CommitEnded, Status: StatusCompleted, Reason: EndCompleted})
		if err := e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.store(id).ReadResult(); err != nil {
			t.Errorf("result.json not written: %v", err)
		}
		if len(e.agents.deletedIDs()) != 2 {
			t.Errorf("deleted %v, want both temporary agents", e.agents.deletedIDs())
		}
		if e.notifier.count() != 1 || len(e.notifier.problems()) != 0 {
			t.Errorf("%d notices, problems %v", e.notifier.count(), e.notifier.problems())
		}
		if e.ctrls.openCount() != 1 {
			t.Error("a terminal forum was opened")
		}
	})
	t.Run("staged removal and a launch that did not start are finished", func(t *testing.T) {
		e := svcSetup(t)
		base := e.scope.BaseDirectory
		staged := uuid.NewString()
		if err := os.MkdirAll(filepath.Join(base, dirCleanup, staged, "layers"), 0o700); err != nil {
			t.Fatal(err)
		}
		abandoned, err := e.svc.NewForum(t.Context(), e.scope)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.svc.SetConfig(t.Context(), e.scope, abandoned, []byte(svcSimpleJSON)); err != nil {
			t.Fatal(err)
		}
		s := svcUnstartedRun(t, e, abandoned, 1)
		if err := e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(base, dirCleanup, staged)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("staged root still there: %v", err)
		}
		if ids := e.forumIDs(); !slices.Equal(ids, []string{abandoned}) || e.status(abandoned) != StatusNew {
			t.Errorf("the forum is not new again: forums %v, status %s", ids, e.status(abandoned))
		}
		if _, err := os.Stat(s.Root()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the run that did not start is still there: %v", err)
		}
		if !slices.Equal(e.agents.deletedIDs(), []string{"leftover"}) {
			t.Errorf("deleted %v, want the abandoned launch's agent", e.agents.deletedIDs())
		}
		if _, ok := e.marker(abandoned, cleanupNotice); ok {
			t.Error("the abandoned launch's notice marker is still there")
		}
		if n, err := e.svc.Launch(t.Context(), abandoned, e.opts()); err != nil || n != 1 {
			t.Errorf("launching again = run %d, %v; want run 1", n, err)
		}
	})
	t.Run("a second launch that did not start leaves the first run as it was", func(t *testing.T) {
		e := svcSetup(t)
		id, c := e.launch(svcSimpleJSON)
		c.finish <- StatusCompleted
		e.settled(id, StatusCompleted)
		svcEventually(t, "notice", func() bool { return e.notifier.count() == 1 })
		e.restart()
		s := svcUnstartedRun(t, e, id, 2)
		if err := e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(s.Root()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("run 2 is still there: %v", err)
		}
		sum, err := e.svc.Status(t.Context(), e.scope, id, 0)
		if err != nil || sum.Run != 1 || sum.Runs != 1 || sum.Status != StatusCompleted {
			t.Errorf("status = %+v (%v), want run 1 of 1, completed", sum, err)
		}
		if e.notifier.count() != 1 {
			t.Errorf("%d notices, want the first run's only", e.notifier.count())
		}
		if n, err := e.svc.Launch(t.Context(), id, e.opts()); err != nil || n != 2 {
			t.Errorf("launching again = run %d, %v; want run 2", n, err)
		}
	})
	t.Run("a forum folder of an earlier version is removed", func(t *testing.T) {
		e := svcSetup(t)
		old := filepath.Join(e.scope.BaseDirectory, uuid.NewString())
		if err := os.MkdirAll(filepath.Join(old, dirCommits), 0o700); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{fileConfig, fileSnapshot} {
			if err := os.WriteFile(filepath.Join(old, f), []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		oldUnlaunched := filepath.Join(e.scope.BaseDirectory, uuid.NewString())
		if err := os.MkdirAll(oldUnlaunched, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oldUnlaunched, "draft.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		ownerless := filepath.Join(e.scope.BaseDirectory, uuid.NewString())
		if err := os.MkdirAll(filepath.Join(ownerless, dirRuns), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ownerless, fileConfig), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{old, oldUnlaunched, ownerless} {
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s is still there: %v", dir, err)
			}
		}
		if calls := e.stuck.list(); len(calls) != 0 {
			t.Errorf("an old folder was reported stuck: %q", calls)
		}
	})
	t.Run("errors are reported and do not stop the scan", func(t *testing.T) {
		e := svcSetup(t)
		bad, _ := e.launch(svcSimpleJSON)
		good, _ := e.launch("")
		e.restart()
		if err := os.WriteFile(e.store(bad).Path(fileConfig), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		err := e.svc.Recover(t.Context(), []Scope{e.scope})
		if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), bad) {
			t.Errorf("Recover = %v", err)
		}
		e.running(good)
		if calls := e.stuck.list(); len(calls) != 1 || !strings.HasPrefix(calls[0], bad+" launcher: ") {
			t.Errorf("OnStuck calls = %q, want the corrupt forum", calls)
		}
	})
	t.Run("forum locked by another process is left alone", func(t *testing.T) {
		e := svcSetup(t)
		id, _ := e.launch("")
		e.restart()
		other := e.store(id)
		if err := other.Lock(); err != nil {
			t.Fatal(err)
		}
		defer other.Unlock()
		if err := e.svc.Recover(t.Context(), []Scope{e.scope}); !errors.Is(err, ErrLocked) {
			t.Errorf("Recover = %v, want ErrLocked", err)
		}
		if calls := e.stuck.list(); len(calls) != 0 {
			t.Errorf("a forum locked elsewhere was reported stuck: %q", calls)
		}
		if _, ok := e.svc.running(e.scope, id); ok {
			t.Error("a locked forum was started")
		}
	})
}

// svcUnstartedRun writes run n of forum id as a launch that died before
// its snapshot leaves it: the run's forum.json, a source, an agents marker
// naming "leftover" and the notice marker.
func svcUnstartedRun(t *testing.T, e *svcEnv, id string, n int) *forumStore {
	t.Helper()
	f, err := openStore(e.scope.BaseDirectory, id)
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.CreateRun(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteConfig([]byte(svcSimpleJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteSource("note", FormatText, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := setAgentsMarker(s, []string{"leftover"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCleanup(cleanupNotice, []byte("pending\n")); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSvcKeepAlive(t *testing.T) {
	e := svcSetup(t)
	e.svc.keepAliveEvery = 10 * time.Millisecond
	id, _ := e.launch("")
	if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusPaused)
	created := e.agents.createdIDs()
	svcEventually(t, "keep-alive touches", func() bool {
		for _, a := range created {
			if e.agents.touches(a) < 2 {
				return false
			}
		}
		return true
	})
	if e.agents.touches("alice") != 0 {
		t.Error("an existing agent was touched")
	}

	e.agents.mu.Lock()
	e.agents.touchErr = errSvcHost
	e.agents.mu.Unlock()
	svcEventually(t, "touch failure logged", func() bool { return e.logger.has("keep-alive of temporary agent") })

	if err := e.svc.Resume(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.running(id)
	if e.keptAlive(id) {
		t.Error("a resumed forum is still kept alive")
	}
}

func TestSvcCloseLeavesStateAndRefusesWork(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch("")
	e.running(id)
	svcClose(t, e.svc)
	if e.status(id) != StatusRunning {
		t.Errorf("status after close = %s, want running", e.status(id))
	}
	if _, err := svcLaunch(t, e.svc, svcConfigJSON, e.opts()); !errors.Is(err, errClosed) {
		t.Errorf("launch after close: %v", err)
	}
	if err := e.svc.Resume(t.Context(), e.scope, id); !errors.Is(err, errClosed) {
		t.Errorf("resume after close: %v", err)
	}
	// The lock was released: another process can take the forum over.
	if err := e.store(id).Lock(); err != nil {
		t.Errorf("lock after close: %v", err)
	}
}

func TestSvcRunErrorLeavesForumInterrupted(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	// Make the next commit fail: a stray file in commits/ is corruption.
	if err := os.WriteFile(filepath.Join(c.store.Path(dirCommits), "stray"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c.store.idx.loaded = false
	c.finish <- StatusCompleted
	svcEventually(t, "run stopped", func() bool {
		_, ok := e.svc.running(e.scope, id)
		return !ok
	})
	if !e.logger.has("ERROR forum " + e.ref(id) + " run 1: stopped") {
		t.Error("the run error was not logged at Error")
	}
	if e.notifier.count() != 0 {
		t.Error("notified after a failed run")
	}
	// The launcher's host hears of the stuck forum once.
	if calls := e.stuck.list(); len(calls) != 1 || !strings.HasPrefix(calls[0], id+" launcher: ") {
		t.Errorf("OnStuck calls = %q", calls)
	}
}

func TestSvcNewPanicsWithoutHost(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New did not panic")
		}
	}()
	New(Host{})
}

// A cancel request accepted just before Run returned paused is settled:
// the service sees the cancelling state and runs the controller again.
func TestSvcRequestLandingAsRunReturnsIsSettled(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	var once sync.Once
	c.beforeReturn = func(c *svcCtrl) {
		once.Do(func() {
			if err := c.RequestCancel(); err != nil {
				t.Errorf("cancel in the window: %v", err)
			}
		})
	}
	if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusCancelled)
	if n := c.runs.Load(); n != 2 {
		t.Errorf("Run called %d times, want 2", n)
	}
	svcEventually(t, "notice", func() bool { return e.notifier.count() == 1 })
	if e.keptAlive(id) {
		t.Error("a cancelled forum is kept alive")
	}
}

// A pause request accepted while Run fails is settled the same way, and
// the forum is not reported stuck.
func TestSvcPauseLandingWhileRunFailsIsSettled(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	var once sync.Once
	c.beforeReturn = func(c *svcCtrl) {
		once.Do(func() {
			if err := c.RequestPause(); err != nil {
				t.Errorf("pause in the window: %v", err)
			}
		})
	}
	c.fail <- errSvcHost
	e.settled(id, StatusPaused)
	if !e.keptAlive(id) {
		t.Error("the paused forum is not kept alive")
	}
	if calls := e.stuck.list(); len(calls) != 0 {
		t.Errorf("OnStuck called for a settled forum: %q", calls)
	}
}

// A request the controller refuses because its Run has just returned is
// carried out on the forum taken over from disk.
func TestSvcRequestRefusedAfterRunExitTakesOver(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	var once sync.Once
	c.onRequest = func(c *svcCtrl) {
		once.Do(func() {
			c.fail <- errSvcHost
			svcEventually(t, "run returned", c.exited.Load)
		})
	}
	if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
		t.Fatalf("pause: %v", err)
	}
	e.settled(id, StatusPaused)
	if n := e.ctrls.openCount(); n != 2 {
		t.Errorf("%d opens, want the take-over's second", n)
	}
}

// The host shutting down is not a stuck forum: logged at Info, no
// OnStuck, the forum resumes at the next start without a failed attempt.
func TestSvcShutdownIsNotStuck(t *testing.T) {
	e := svcSetup(t)
	var down atomic.Bool
	down.Store(true)
	replier := &svcReplier{asks: map[string]int{}}
	messenger := svcMessengerFunc(func(ctx context.Context, agentID, msg string, wait time.Duration) (Reply, error) {
		if down.Load() {
			return Reply{}, fmt.Errorf("agent loop stopping: %w", ErrShuttingDown)
		}
		return replier.Ask(ctx, agentID, msg, wait)
	})
	newSvc := func() *Service {
		svc := New(Host{Messenger: messenger, Agents: e.agents, Notifier: e.notifier, Logger: e.logger, OnStuck: e.stuck.record})
		t.Cleanup(func() { svcClose(t, svc) })
		return svc
	}
	svc := newSvc()
	id, err := svcLaunch(t, svc, svcSimpleJSON, e.opts())
	if err != nil {
		t.Fatal(err)
	}
	svcEventually(t, "run stopped", func() bool {
		_, ok := svc.running(e.scope, id)
		return !ok
	})
	if !e.logger.has("INFO forum "+e.ref(id)+" run 1: stopped by the shutdown") || e.logger.has("ERROR forum "+e.ref(id)) {
		t.Error("the shutdown was not logged at Info only")
	}
	if calls := e.stuck.list(); len(calls) != 0 {
		t.Errorf("OnStuck = %q", calls)
	}
	attempts, err := e.store(id).ListAttempts("talk")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		if a.Reply != nil {
			t.Errorf("a reply was recorded for the shutdown: %+v", a)
		}
	}
	svcClose(t, svc)
	down.Store(false)
	svc = newSvc()
	if err := svc.Recover(t.Context(), []Scope{e.scope}); err != nil {
		t.Fatal(err)
	}
	svcEventually(t, "completion notice", func() bool { return e.notifier.count() == 1 })
	if res, _ := e.notifier.last(); res.Status != StatusCompleted {
		t.Errorf("result = %+v", res)
	}
}

// svcMessengerFunc adapts a function to Messenger.
type svcMessengerFunc func(ctx context.Context, agentID, message string, wait time.Duration) (Reply, error)

func (f svcMessengerFunc) Ask(ctx context.Context, agentID, message string, wait time.Duration) (Reply, error) {
	return f(ctx, agentID, message, wait)
}

// Temporary agents that could not be deleted at the terminal state are
// retried by the keep-alive loop until they are gone.
func TestSvcCleanupIsRetried(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	created := e.agents.createdIDs()
	e.agents.setDeleteErr(created[0], errSvcHost)
	c.finish <- StatusCompleted
	e.settled(id, StatusCompleted)
	if _, ok := e.marker(id, cleanupAgents); !ok {
		t.Fatal("the agents marker is gone although a deletion failed")
	}
	if e.cleanupsPending() != 1 {
		t.Fatal("the failed deletion is not queued for a retry")
	}
	e.svc.keepAliveTick(t.Context()) // still failing: kept
	if _, ok := e.marker(id, cleanupAgents); !ok || e.cleanupsPending() != 1 {
		t.Fatal("a failed retry dropped the deletion")
	}
	e.agents.setDeleteErr(created[0], nil)
	e.svc.keepAliveTick(t.Context())
	if _, ok := e.marker(id, cleanupAgents); ok || !slices.Contains(e.agents.deletedIDs(), created[0]) {
		t.Error("the retry did not delete the agent")
	}
	if e.cleanupsPending() != 0 {
		t.Error("the retry set is not empty")
	}
}

// A temporary agent the host deletes when its turn ends is not a failure: it
// is logged at Info, the notice is still sent, and it stays in the marker and
// is retried, so a restart before the turn ends still deletes it.
func TestSvcCleanupPendingTurn(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	created := e.agents.createdIDs()
	e.agents.setDeleteErr(created[0], fmt.Errorf("%w: busy", ErrDeletePending))
	c.finish <- StatusCompleted
	e.settled(id, StatusCompleted)
	svcEventually(t, "notice", func() bool { return e.notifier.count() == 1 })
	if !e.logger.has("are deleted when their turns end") || e.logger.has("retried every") {
		t.Errorf("pending deletion logged as a failure: %v", e.logger.lines)
	}
	if _, ok := e.marker(id, cleanupAgents); !ok {
		t.Fatal("the agents marker is gone although a deletion is pending")
	}
	// Retries while the turn still runs keep it listed.
	for range 3 {
		e.svc.keepAliveTick(t.Context())
	}
	if _, ok := e.marker(id, cleanupAgents); !ok || e.cleanupsPending() != 1 {
		t.Fatalf("a pending deletion was dropped by a retry (marker %v, retried %d)", ok, e.cleanupsPending())
	}
	e.agents.setDeleteErr(created[0], nil) // the turn ended: the agent is gone
	e.svc.keepAliveTick(t.Context())
	if _, ok := e.marker(id, cleanupAgents); ok || e.cleanupsPending() != 0 {
		t.Error("the retry did not clear the marker")
	}
}

// The temporary agents of a running forum are touched too.
func TestSvcKeepAliveTouchesRunningForums(t *testing.T) {
	e := svcSetup(t)
	e.svc.keepAliveEvery = 10 * time.Millisecond
	id, _ := e.launch("")
	e.running(id)
	created := e.agents.createdIDs()
	svcEventually(t, "running forum's agents touched", func() bool {
		for _, a := range created {
			if e.agents.touches(a) < 1 {
				return false
			}
		}
		return true
	})
}

// A run error other than a shutdown is reported through OnStuck once,
// however often the forum stops again in this process.
func TestSvcStuckIsReportedOnce(t *testing.T) {
	e := svcSetup(t)
	id, c := e.launch("")
	c.fail <- errSvcHost
	svcEventually(t, "run stopped", func() bool {
		_, ok := e.svc.running(e.scope, id)
		return !ok
	})
	if !e.logger.has("ERROR forum " + e.ref(id) + " run 1: stopped") {
		t.Error("not logged at Error naming the forum")
	}
	if err := e.svc.Resume(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	c = e.ctrls.get(t, id)
	c.fail <- errSvcHost
	svcEventually(t, "second stop", func() bool {
		_, ok := e.svc.running(e.scope, id)
		return !ok && e.ctrls.openCount() == 2
	})
	if calls := e.stuck.list(); len(calls) != 1 || !strings.HasPrefix(calls[0], id+" launcher: ") || !strings.Contains(calls[0], errSvcHost.Error()) {
		t.Errorf("OnStuck calls = %q, want one", calls)
	}
}

// A Notifier that blocks never holds the forum: the run is released and
// the forum can be deleted while the notice is still being delivered.
func TestSvcBlockingNotifierDoesNotHoldTheForum(t *testing.T) {
	e := svcSetup(t)
	e.notifier.block = make(chan struct{})
	id, c := e.launch("")
	c.finish <- StatusCompleted
	e.settled(id, StatusCompleted)
	if err := e.svc.Delete(t.Context(), e.scope, id); err != nil {
		t.Fatalf("delete while the notice is pending: %v", err)
	}
	close(e.notifier.block)
	svcEventually(t, "notice", func() bool { return e.notifier.count() == 1 })
}

// Control locks are dropped once no operation uses them, and status and
// list read forums without verifying every digest.
func TestSvcControlsPrunedAndListIsLight(t *testing.T) {
	e := svcSetup(t)
	id, _ := e.launch("")
	if err := e.svc.Pause(t.Context(), e.scope, id); err != nil {
		t.Fatal(err)
	}
	e.settled(id, StatusPaused)
	if err := e.svc.Delete(t.Context(), e.scope, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	e.svc.mu.Lock()
	n := len(e.svc.controls)
	e.svc.mu.Unlock()
	if n != 0 {
		t.Errorf("%d control locks left", n)
	}

	// A damaged source fails verify but not status or list.
	snap, err := e.store(id).ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(e.store(id).Path(snap.Sources["note"].File), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = verify(e.store(id)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Verify: %v", err)
	}
	list, err := e.svc.List(t.Context(), e.scope)
	if err != nil || len(list) != 1 || list[0].Status != StatusPaused {
		t.Errorf("List = %+v, %v", list, err)
	}
}

// Results of a running forum hide a round that is not yet published,
// exactly as result.json would.
func TestSvcResultsHidePartialRound(t *testing.T) {
	e := svcSetup(t)
	release := make(chan struct{})
	replier := &svcReplier{asks: map[string]int{}}
	messenger := svcMessengerFunc(func(ctx context.Context, agentID, msg string, wait time.Duration) (Reply, error) {
		if agentID != "alice" && strings.Contains(msg, "round 2") {
			select {
			case <-release:
			case <-ctx.Done():
				return Reply{}, ctx.Err()
			}
		}
		return replier.Ask(ctx, agentID, msg, wait)
	})
	svc := New(Host{Messenger: messenger, Agents: e.agents, Notifier: e.notifier, Logger: e.logger})
	t.Cleanup(func() { svcClose(t, svc) })
	cfg := strings.Replace(svcSimpleJSON, `"max_rounds": 1`, `"max_rounds": 2`, 1)
	id, err := svcLaunch(t, svc, cfg, e.opts())
	if err != nil {
		t.Fatal(err)
	}
	var res *Result
	svcEventually(t, "alice's round 2 committed", func() bool {
		res, err = svc.Results(t.Context(), e.scope, id, 0)
		return err == nil && slices.Contains(res.Omissions, "layer talk round 2: 1 committed output(s) not published (round incomplete)")
	})
	for _, o := range res.Layers[0].Outputs {
		if o.Round != 1 {
			t.Errorf("an unpublished round-2 output is in the partial result: %+v", o)
		}
	}
	if len(res.Layers[0].Outputs) != 2 || res.Complete {
		t.Errorf("partial result = %+v", res)
	}
	close(release)
	svcEventually(t, "completion notice", func() bool { return e.notifier.count() == 1 })
}

// A run whose result layers have no output lists the other layers'
// outputs, so the work done is reachable: once it has ended every
// committed output, while it runs only published ones.
func TestSvcResultListsOtherLayersWhenTheResultIsEmpty(t *testing.T) {
	cfg, err := decodeConfig([]byte(strings.Replace(svcSimpleJSON, `"layers": [`, `"layers": [
    {"id": "answer", "participants": ["alice", "bob"], "instructions": "Answer.",
     "delivery": "after_round", "max_rounds": 1, "output": {"format": "text"}},`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	snap := &Snapshot{ForumID: "f", Layers: []string{"answer", "talk"}, ResultLayers: []string{"talk"}}
	answer := OutputRecord{OutputID: "a1", LayerID: "answer", Round: 1, ParticipantID: "alice", Turn: turnID(1, "alice")}
	st := &State{
		Status: StatusFailed, Reason: EndAttemptsExhausted,
		Layers: map[string]*LayerState{"answer": {Started: true, Round: 1, Outputs: []OutputRecord{answer}}},
	}
	res := buildResult(cfg, snap, st)
	if len(res.OtherLayers) != 1 || res.OtherLayers[0].LayerID != "answer" || len(res.OtherLayers[0].Outputs) != 1 {
		t.Errorf("other layers of a failed run = %+v", res.OtherLayers)
	}
	st.Status, st.Reason = StatusRunning, ""
	if res = buildResult(cfg, snap, st); len(res.OtherLayers) != 0 {
		t.Errorf("a running run lists an unpublished round: %+v", res.OtherLayers)
	}
	st.Status = StatusCompleted
	st.Layers["talk"] = &LayerState{Started: true, Ended: true, RoundsPublished: 1, Outputs: []OutputRecord{
		{OutputID: "t1", LayerID: "talk", Round: 1, ParticipantID: "alice", Turn: turnID(1, "alice")},
	}}
	if res = buildResult(cfg, snap, st); len(res.OtherLayers) != 0 {
		t.Errorf("a result with outputs lists other layers: %+v", res.OtherLayers)
	}
}

// Validation reports every problem at once: an empty configuration names
// the version and each missing part, not only the first.
func TestSvcValidateReportsEveryProblem(t *testing.T) {
	e := svcSetup(t)
	err := e.svc.Validate(t.Context(), []byte(`{}`), e.opts())
	ve, ok := errors.AsType[*ValidationError](err)
	if !ok {
		t.Fatalf("Validate({}) = %v", err)
	}
	paths := map[string]bool{}
	for _, is := range ve.Issues {
		paths[strings.SplitN(is.Path, ".", 2)[0]] = true
	}
	for _, want := range []string{"version", "brief", "participants", "layers", "limits"} {
		if !paths[want] {
			t.Errorf("no issue about %s in %v", want, ve.Issues)
		}
	}
}

// Close while Recover is starting runs never adds a goroutine to the
// WaitGroup Close is waiting on (a data race and a possible panic), and no
// run is left live once both have returned.
func TestSvcCloseDuringRecover(t *testing.T) {
	for range 20 {
		e := svcSetup(t)
		for range 4 {
			e.launch("")
		}
		e.restart()
		svc := e.svc
		recovered := make(chan struct{})
		go func() {
			defer close(recovered)
			// Whatever Recover reports (a run refused because the service
			// closed), it must not race Close.
			if err := svc.Recover(context.WithoutCancel(t.Context()), []Scope{e.scope}); err != nil {
				t.Logf("recover: %v", err)
			}
		}()
		svcClose(t, svc)
		<-recovered
		svc.mu.Lock()
		live := len(svc.runs)
		svc.mu.Unlock()
		if live != 0 {
			t.Fatalf("%d runs live after Close and Recover returned", live)
		}
		if err := svc.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

// Once Close has been called, Recover starts nothing: no run and no
// completion notice, so nothing outlives Close.
func TestSvcRecoverAfterCloseStartsNothing(t *testing.T) {
	e := svcSetup(t)
	e.notifier.block = make(chan struct{})
	ended, c := e.launch("")
	c.finish <- StatusCompleted
	e.settled(ended, StatusCompleted)
	running, _ := e.launch("")
	e.restart() // the blocked notice is cut by the shutdown and stays pending
	close(e.notifier.block)
	if _, ok := e.marker(ended, cleanupNotice); !ok {
		t.Fatal("the notice is not pending after the restart")
	}
	before := e.notifier.count()
	svcClose(t, e.svc)
	if err := e.svc.Recover(t.Context(), []Scope{e.scope}); !errors.Is(err, errClosed) {
		t.Errorf("Recover after Close = %v, want errClosed", err)
	}
	if _, ok := e.svc.running(e.scope, running); ok {
		t.Error("Recover started a run after Close")
	}
	e.svc.mu.Lock()
	notifying := len(e.svc.notifying)
	e.svc.mu.Unlock()
	if notifying != 0 || e.notifier.count() != before {
		t.Errorf("Recover started a notice after Close (in flight %d, sent %d, before %d)", notifying, e.notifier.count(), before)
	}
}
