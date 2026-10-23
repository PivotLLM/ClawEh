// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/PivotLLM/ClawEh/forum/forumfs"
)

// Helpers in this file are prefixed st so they cannot collide with other
// tests' helpers in the same package.

// stConfig is a two-layer forum: "debate" (after_round, two rounds,
// moderated, JSON output with share) and "summary" (per_turn, one round,
// markdown).
func stConfig() *Config {
	share := []string{"/claim"}
	return &Config{
		Version: configVersion,
		Name:    "store test",
		Brief:   Brief{Purpose: "test the store", Task: "argue"},
		Sources: map[string]Source{"notes": {Decode: FormatText, Inline: json.RawMessage(`"background notes"`)}},
		Participants: map[string]Participant{
			"alice": {Agent: "alice", Instructions: "PRIVATE-INSTRUCTIONS-ALICE"},
			"bob":   {Model: "model-b", Instructions: "PRIVATE-INSTRUCTIONS-BOB"},
			"mod":   {Clone: "alice"},
		},
		Layers: []Layer{
			{
				ID: "debate", Participants: []string{"alice", "bob"}, Instructions: "debate it",
				Delivery: DeliveryAfterRound, MaxRounds: 2,
				Output:    Output{Format: FormatJSON, Share: &share},
				Moderator: &Moderator{Participant: "mod", AfterRound: 1, EveryRounds: 1},
			},
			{
				ID: "summary", Participants: []string{"alice", "bob"}, Instructions: "summarise",
				Inputs:   []Route{{From: "layer:debate"}},
				Delivery: DeliveryPerTurn, MaxRounds: 1,
				Output: Output{Format: FormatMarkdown},
			},
		},
		Limits: Limits{MaxCalls: 50, MaxDurationSeconds: 600, CallTimeoutSeconds: 60, MaxAttemptsPerTurn: 3, MaxParallelCalls: 2},
	}
}

// stNewStore creates a forum under a fresh base directory and returns the
// store of its empty run 1.
func stNewStore(t *testing.T) *forumStore {
	t.Helper()
	return stNewRun(t, t.TempDir())
}

// stNewRun creates a forum under base and returns the store of its empty
// run 1.
func stNewRun(t *testing.T, base string) *forumStore {
	t.Helper()
	f, err := createStore(base, uuid.NewString())
	if err != nil {
		t.Fatalf("CreateStore: %v", err)
	}
	if err = f.WriteForumMeta(&ForumMeta{Owner: "launcher"}); err != nil {
		t.Fatalf("WriteForumMeta: %v", err)
	}
	if err = f.WriteForumConfig([]byte("{}\n")); err != nil {
		t.Fatalf("WriteForumConfig: %v", err)
	}
	s, err := f.CreateRun(1)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return s
}

// stOpenRun opens s's run again through openStore and OpenRun.
func stOpenRun(s *forumStore) (*forumStore, error) {
	f, err := openStore(s.base, s.ID())
	if err != nil {
		return nil, err
	}
	return f.OpenRun(s.RunNumber())
}

// stReopen is another handle of s's forum and run with a lock of its own,
// as another process would open it.
func stReopen(s *forumStore) *forumStore {
	h := newForumHandle(s.base, s.id)
	if s.run == 0 {
		return h
	}
	return h.Run(s.run)
}

// stLaunch writes everything Launch writes (forum.json, the source,
// participants.json, snapshot.json) and the launched commit.
func stLaunch(t *testing.T, s *forumStore, cfg *Config) *Snapshot {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err = s.WriteConfig(raw); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	src, err := s.WriteSource("notes", FormatText, []byte("background notes"))
	if err != nil {
		t.Fatalf("WriteSource: %v", err)
	}
	parts := &Participants{Participants: map[string]ParticipantRecord{
		"alice": {ID: "alice", Form: FormExisting, AgentID: "alice", Name: "Alice"},
		"bob":   {ID: "bob", Form: FormFresh, AgentID: uuid.NewString(), Created: true, Name: "Bob", Model: "model-b"},
		"mod":   {ID: "mod", Form: FormClone, AgentID: uuid.NewString(), Created: true, Name: "mod"},
	}}
	if err := s.WriteParticipants(parts); err != nil {
		t.Fatalf("WriteParticipants: %v", err)
	}
	launched := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	snap := &Snapshot{
		ForumID: s.ID(), Run: s.RunNumber(), Name: cfg.Name, LaunchedAt: launched, BaseDirectory: s.base,
		Deadline: launched.Add(10 * time.Minute), ConfigDigest: digest(raw), Seed: 7, Limits: cfg.Limits,
		Layers: []string{"debate", "summary"}, ResultLayers: []string{"summary"},
		Models:  map[string]string{"bob": "model-b"},
		Sources: map[string]SourceRecord{"notes": src},
	}
	if err := s.WriteSnapshot(snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	if _, err := nextAppend(s, &Commit{Kind: CommitLaunched}); err != nil {
		t.Fatalf("AppendCommit(launched): %v", err)
	}
	return snap
}

// nextAppend appends c at the seq after the last commit on disk, as a
// caller that tracks the log's seq would.
func nextAppend(s *forumStore, c *Commit) (int, error) {
	commits, err := s.ReadCommits()
	if err != nil {
		return 0, err
	}
	seq := len(commits) + 1 // seqs are contiguous from 1 (ReadCommits checks)
	return seq, s.AppendCommit(seq, c)
}

// stReserve writes an attempt's request and commits its reservation.
func stReserve(t *testing.T, s *forumStore, layer string, round int, turn string, kind TurnKind, participant string, attempt, through int, message string) {
	t.Helper()
	req := &AttemptRequest{
		Layer: layer, Round: round, Turn: turn, Attempt: attempt, Kind: kind, Participant: participant,
		AgentID: participant, SentAt: time.Now().UTC(), WaitSeconds: 60, Repair: attempt > 1, ThroughSeq: through, Message: message,
	}
	if err := s.WriteAttemptRequest(req); err != nil {
		t.Fatalf("WriteAttemptRequest(%s/%s/%d): %v", layer, turn, attempt, err)
	}
	if _, err := nextAppend(s, &Commit{
		Kind: CommitAttempt, Layer: layer, Round: round, Turn: turn, TurnKind: kind,
		Participant: participant, Attempt: attempt, ThroughSeq: through,
	}); err != nil {
		t.Fatalf("AppendCommit(attempt %s/%s/%d): %v", layer, turn, attempt, err)
	}
}

// stReply writes an attempt's reply.
func stReply(t *testing.T, s *forumStore, layer, turn string, attempt int, text string, issues []string) {
	t.Helper()
	if err := s.WriteAttemptReply(layer, turn, attempt, &AttemptReply{
		ReceivedAt: time.Now().UTC(), Outcome: OutcomeOK, Text: text, Issues: issues,
	}); err != nil {
		t.Fatalf("WriteAttemptReply(%s/%s/%d): %v", layer, turn, attempt, err)
	}
}

func stIsPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s: mode %o, want %o", path, got, want)
	}
}

func TestCreateStoreLayout(t *testing.T) {
	s := stNewStore(t)
	base := s.base
	for _, d := range []string{
		s.Dir(), filepath.Join(s.Dir(), dirRuns), s.Root(), filepath.Join(base, forumfs.LocksDir), filepath.Join(base, dirCleanup),
		s.Path(dirSources), s.Path(dirLayers), s.Path(dirCommits),
	} {
		stIsPerm(t, d, dirPerm)
	}
	stIsPerm(t, filepath.Join(s.Dir(), fileConfig), filePerm)
	if s.ID() != filepath.Base(s.Dir()) || s.Root() != filepath.Join(s.Dir(), dirRuns, "1") || s.RunNumber() != 1 {
		t.Errorf("forum %s, run %d in %s", s.ID(), s.RunNumber(), s.Root())
	}
}

// Runs are listed in order; a run directory without a snapshot did not
// start, and RemoveRun takes a run out in one step, leaving the others.
func TestStoreRuns(t *testing.T) {
	first := stNewStore(t)
	f, err := openStore(first.base, first.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Lock(); err != nil {
		t.Fatal(err)
	}
	defer f.Unlock()
	if !f.Run(1).locked() {
		t.Error("a run's handle does not share the forum's lock")
	}
	for _, n := range []int{2, 10} {
		if _, err := f.CreateRun(n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.CreateRun(2); err == nil {
		t.Error("CreateRun of an existing run succeeded")
	}
	if err := os.Mkdir(filepath.Join(f.Dir(), dirRuns, "notes"), dirPerm); err != nil {
		t.Fatal(err)
	}
	if runs, err := f.Runs(); err != nil || !slices.Equal(runs, []int{1, 2, 10}) {
		t.Errorf("Runs = %v, %v", runs, err)
	}
	stLaunch(t, f.Run(2), stConfig())
	if started, err := startedRuns(f); err != nil || !slices.Equal(started, []int{2}) {
		t.Errorf("startedRuns = %v, %v", started, err)
	}
	if err := f.Run(10).RemoveRun(); err != nil {
		t.Fatal(err)
	}
	if runs, err := f.Runs(); err != nil || !slices.Equal(runs, []int{1, 2}) {
		t.Errorf("Runs after RemoveRun = %v, %v", runs, err)
	}
	if _, err := f.OpenRun(10); !errors.Is(err, ErrNotFound) {
		t.Errorf("OpenRun of a removed run = %v", err)
	}
	if err := f.Run(2).SetCleanup(cleanupAgents, []byte("[]")); err != nil {
		t.Fatal(err)
	}
	if marked, err := f.MarkedRuns(cleanupAgents); err != nil || !slices.Equal(marked, []int{2}) {
		t.Errorf("MarkedRuns = %v, %v", marked, err)
	}
	if err := f.SetCleanup(cleanupAgents, nil); err == nil {
		t.Error("the forum handle wrote a run marker")
	}
	if _, err := f.ReadConfig(); err == nil {
		t.Error("the forum handle read a run file")
	}
}

func TestCreateStoreRejects(t *testing.T) {
	base := t.TempDir()
	id := uuid.NewString()
	if _, err := createStore(base, id); err != nil {
		t.Fatalf("CreateStore: %v", err)
	}
	tests := []struct {
		name string
		base string
		id   string
	}{
		{"relative base", "forums", uuid.NewString()},
		{"existing root", base, id},
		{"not a UUID", base, "alice"},
		{"path traversal", base, "../" + uuid.NewString()},
		{"upper-case UUID", base, strings.ToUpper(uuid.NewString())},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := createStore(tt.base, tt.id); err == nil {
				t.Fatal("CreateStore succeeded, want an error")
			}
		})
	}
}

func TestOpenStore(t *testing.T) {
	s := stNewStore(t)
	base := s.base

	got, err := openStore(base, s.ID())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if got.Dir() != s.Dir() || got.ID() != s.ID() || got.RunNumber() != 0 {
		t.Errorf("opened %s/%s run %d, want %s/%s run 0", got.Dir(), got.ID(), got.RunNumber(), s.Dir(), s.ID())
	}
	if run, err := got.OpenRun(1); err != nil || run.Root() != s.Root() {
		t.Errorf("OpenRun(1) = %v, %v", run, err)
	}

	for _, id := range []string{uuid.NewString(), "../" + s.ID(), "x"} {
		if _, err := openStore(base, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("OpenStore(%q): %v, want ErrNotFound", id, err)
		}
	}
	if _, err := openStore("relative", s.ID()); err == nil {
		t.Error("OpenStore with a relative base succeeded")
	}

	// A root that is a file, a run missing commits/ and a forum missing
	// runs/ are corrupt.
	fileRoot := uuid.NewString()
	if err := os.WriteFile(filepath.Join(base, fileRoot), nil, filePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(base, fileRoot); !errors.Is(err, ErrCorrupt) {
		t.Errorf("root is a file: %v, want ErrCorrupt", err)
	}
	if err := os.RemoveAll(s.Path(dirCommits)); err != nil {
		t.Fatal(err)
	}
	if _, err := got.OpenRun(1); !errors.Is(err, ErrCorrupt) {
		t.Errorf("missing commits/: %v, want ErrCorrupt", err)
	}
	if err := os.RemoveAll(filepath.Join(s.Dir(), dirRuns)); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(base, s.ID()); !errors.Is(err, ErrCorrupt) {
		t.Errorf("missing runs/: %v, want ErrCorrupt", err)
	}
	if err := os.Remove(filepath.Join(s.Dir(), fileMeta)); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(base, s.ID()); !errors.Is(err, ErrNotFound) {
		t.Errorf("open without forum-meta.json: %v, want ErrNotFound", err)
	}
	if err := os.Remove(filepath.Join(s.Dir(), fileConfig)); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(base, s.ID()); !errors.Is(err, ErrNotFound) {
		t.Errorf("open without forum.json: %v, want ErrNotFound", err)
	}
}

func TestListForums(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing")
	if ids, err := listForums(missing); err != nil || len(ids) != 0 {
		t.Fatalf("ListForums(missing) = %v, %v; want empty", ids, err)
	}
	base := t.TempDir()
	want := make([]string, 0, 3)
	for range 3 {
		s, err := createStore(base, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.WriteForumMeta(&ForumMeta{Owner: "launcher"}); err != nil {
			t.Fatal(err)
		}
		if err := s.WriteForumConfig([]byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		want = append(want, s.ID())
	}
	// Not listed either: a configuration without its owner record.
	ownerless, createErr := createStore(base, uuid.NewString())
	if createErr != nil {
		t.Fatal(createErr)
	}
	if err := ownerless.WriteForumConfig([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if incomplete, err := listIncomplete(base); err != nil || !slices.Contains(incomplete, ownerless.ID()) {
		t.Errorf("ListIncomplete = %v, %v; want the forum without an owner", incomplete, err)
	}
	// Not listed: a root whose creation died before forum.json, a non-UUID
	// directory, and a plain file named like a forum.
	if _, err := createStore(base, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "notes"), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, uuid.NewString()), nil, filePerm); err != nil {
		t.Fatal(err)
	}
	got, err := listForums(base)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListForums = %v, want %v", got, want)
	}
}

func TestStoreRecordsRoundTrip(t *testing.T) {
	s := stNewStore(t)
	cfg := stConfig()
	snap := stLaunch(t, s, cfg)

	raw, err := s.ReadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if digest(raw) != snap.ConfigDigest {
		t.Error("forum.json is not the bytes written")
	}
	if err = s.WriteConfig(raw); !errors.Is(err, os.ErrExist) {
		t.Errorf("second WriteConfig: %v, want os.ErrExist", err)
	}

	gotSnap, err := s.ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotSnap, snap) {
		t.Errorf("snapshot round trip:\n got %+v\nwant %+v", gotSnap, snap)
	}
	if err = s.WriteSnapshot(snap); !errors.Is(err, os.ErrExist) {
		t.Errorf("second WriteSnapshot: %v, want os.ErrExist", err)
	}

	parts, err := s.ReadParticipants()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts.Participants) != 3 || !parts.Participants["bob"].Created {
		t.Errorf("participants round trip: %+v", parts)
	}
	// After the launched commit participants.json is frozen.
	if err = s.WriteParticipants(parts); !errors.Is(err, ErrInvalidState) {
		t.Errorf("WriteParticipants after launch: %v, want ErrInvalidState", err)
	}

	src := snap.Sources["notes"]
	if src.File != "sources/notes.txt" || src.Decode != FormatText {
		t.Errorf("source record %+v", src)
	}
	data, err := s.ReadFile(src.File)
	if err != nil || string(data) != "background notes" || digest(data) != src.Digest {
		t.Errorf("source content %q, %v", data, err)
	}
	if _, err = s.WriteSource("notes", FormatText, []byte("again")); !errors.Is(err, os.ErrExist) {
		t.Errorf("second WriteSource: %v, want os.ErrExist", err)
	}
	if _, err = s.WriteSource("../escape", FormatText, nil); err == nil {
		t.Error("WriteSource accepted a path as ID")
	}

	if _, err = s.ReadLayerInputs("debate"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadLayerInputs before write: %v, want ErrNotFound", err)
	}
	in := &LayerInputs{LayerID: "debate", Participants: map[string][]InputItem{
		"alice": {{Route: 0, Kind: InputSource, SourceID: "notes", Format: FormatText, Content: "background notes"}},
		"bob":   {},
	}, Missing: []int{1}}
	if err = s.WriteLayerInputs(in); err != nil {
		t.Fatal(err)
	}
	gotIn, err := s.ReadLayerInputs("debate")
	if err != nil || !reflect.DeepEqual(gotIn, in) {
		t.Errorf("inputs round trip: %+v, %v", gotIn, err)
	}
	if err = s.WriteLayerInputs(in); !errors.Is(err, os.ErrExist) {
		t.Errorf("second WriteLayerInputs: %v, want os.ErrExist", err)
	}
	if err = s.WriteLayerInputs(&LayerInputs{LayerID: "../x"}); err == nil {
		t.Error("WriteLayerInputs accepted a path as layer ID")
	}

	if _, err = s.ReadState(); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadState before write: %v, want ErrNotFound", err)
	}
	st := &State{
		Status: StatusRunning, Seq: 1, Layers: map[string]*LayerState{"debate": {Outputs: []OutputRecord{}}},
		UpdatedAt: time.Now().UTC().Round(0),
	}
	if err = s.WriteState(st); !errors.Is(err, ErrInvalidState) {
		t.Errorf("WriteState without the lock: %v, want ErrInvalidState", err)
	}
	if err = s.Lock(); err != nil {
		t.Fatal(err)
	}
	defer s.Unlock()
	for range 2 { // the lock holder rewrites state.json freely
		if err = s.WriteState(st); err != nil {
			t.Fatal(err)
		}
	}
	gotSt, err := s.ReadState()
	if err != nil || !reflect.DeepEqual(gotSt, st) {
		t.Errorf("state round trip: %+v, %v", gotSt, err)
	}

	if _, err = s.ReadResult(); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadResult before write: %v, want ErrNotFound", err)
	}
	res := &Result{ForumID: s.ID(), Status: StatusCompleted, Complete: true, Layers: []LayerResult{}, Transcript: fileTranscript}
	if err = s.WriteResult(res); err != nil {
		t.Fatal(err)
	}
	if err = s.WriteResult(res); !errors.Is(err, os.ErrExist) {
		t.Errorf("second WriteResult: %v, want os.ErrExist", err)
	}
	gotRes, err := s.ReadResult()
	if err != nil || gotRes.Status != StatusCompleted || !gotRes.Complete {
		t.Errorf("result round trip: %+v, %v", gotRes, err)
	}

	// A record that does not decode is corrupt, not missing.
	if err := os.WriteFile(s.Path(fileSnapshot), []byte("{"), filePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadSnapshot(); !errors.Is(err, ErrCorrupt) {
		t.Errorf("truncated snapshot: %v, want ErrCorrupt", err)
	}
}

func TestStoreWriteParticipantsBeforeLaunch(t *testing.T) {
	s := stNewStore(t)
	p := &Participants{Participants: map[string]ParticipantRecord{"alice": {ID: "alice", Form: FormExisting, AgentID: "alice"}}}
	for range 2 {
		if err := s.WriteParticipants(p); err != nil {
			t.Fatalf("WriteParticipants before launch: %v", err)
		}
	}
	if _, err := s.ReadParticipants(); err != nil {
		t.Fatal(err)
	}
	empty := stNewStore(t)
	if _, err := empty.ReadParticipants(); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadParticipants absent: %v, want ErrNotFound", err)
	}
}

func TestStoreAttempts(t *testing.T) {
	s := stNewStore(t)
	stLaunch(t, s, stConfig())

	if got, err := s.ListAttempts("debate"); err != nil || len(got) != 0 {
		t.Fatalf("ListAttempts on an untouched layer = %v, %v", got, err)
	}

	// Bob's turn takes ten attempts (numeric order must hold past 9);
	// Alice's one. Alice's turn ID sorts first.
	bobTurn, aliceTurn := turnID(1, "bob"), turnID(1, "alice")
	for n := 1; n <= 10; n++ {
		stReserve(t, s, "debate", 1, bobTurn, TurnParticipant, "bob", n, 1, "message "+strconv.Itoa(n))
		if n < 10 {
			stReply(t, s, "debate", bobTurn, n, "bad", []string{"not JSON"})
		}
	}
	stReserve(t, s, "debate", 1, aliceTurn, TurnParticipant, "alice", 1, 1, "hello")
	stReply(t, s, "debate", aliceTurn, 1, `{"claim":"x"}`, nil)

	got, err := s.ListAttempts("debate")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 11 {
		t.Fatalf("ListAttempts returned %d attempts, want 11", len(got))
	}
	if got[0].Request.Turn != aliceTurn || got[0].Reply == nil || got[0].Reply.Text != `{"claim":"x"}` {
		t.Errorf("first attempt %+v", got[0])
	}
	for i, a := range got[1:] {
		if a.Request.Turn != bobTurn || a.Request.Attempt != i+1 {
			t.Errorf("attempt %d is %s/%d", i+1, a.Request.Turn, a.Request.Attempt)
		}
	}
	if got[10].Reply != nil {
		t.Error("the unanswered attempt has a reply")
	}
	if got[1].Reply == nil || got[1].Reply.Issues[0] != "not JSON" {
		t.Errorf("rejected reply not kept: %+v", got[1].Reply)
	}

	// A reserved attempt cannot be written again, nor answered twice.
	err = s.WriteAttemptRequest(&AttemptRequest{Layer: "debate", Turn: aliceTurn, Attempt: 1})
	if !errors.Is(err, os.ErrExist) {
		t.Errorf("rewriting a reserved attempt: %v, want os.ErrExist", err)
	}
	if err := s.WriteAttemptReply("debate", aliceTurn, 1, &AttemptReply{}); !errors.Is(err, os.ErrExist) {
		t.Errorf("second reply: %v, want os.ErrExist", err)
	}
	// A reply to an attempt that was never reserved breaks invariant 3.
	if err := s.WriteAttemptReply("debate", aliceTurn, 2, &AttemptReply{}); !errors.Is(err, ErrInvalidState) {
		t.Errorf("reply to an unreserved attempt: %v, want ErrInvalidState", err)
	}
	for _, bad := range []*AttemptRequest{
		{Layer: "../x", Turn: aliceTurn, Attempt: 1},
		{Layer: "debate", Turn: "a/b", Attempt: 1},
		{Layer: "debate", Turn: aliceTurn, Attempt: 0},
	} {
		if err := s.WriteAttemptRequest(bad); err == nil {
			t.Errorf("WriteAttemptRequest(%+v) succeeded", bad)
		}
	}
	if _, err := s.ListAttempts("../x"); err == nil {
		t.Error("ListAttempts accepted a path as layer ID")
	}
}

// A crash between request.json and its CommitAttempt leaves an orphan
// attempt directory. It is not a reserved attempt, so it is not listed,
// and the controller's next WriteAttemptRequest for that number replaces
// it.
func TestStoreOrphanAttemptRecovery(t *testing.T) {
	s := stNewStore(t)
	stLaunch(t, s, stConfig())
	turn := turnID(1, "alice")
	orphan := &AttemptRequest{Layer: "debate", Round: 1, Turn: turn, Attempt: 1, Kind: TurnParticipant, Participant: "alice", Message: "never sent"}
	if err := s.WriteAttemptRequest(orphan); err != nil {
		t.Fatal(err)
	}
	// The crash also left an attempt directory being assembled.
	if err := os.Mkdir(filepath.Join(s.Path("layers/debate/calls/"+turn), forumfs.TempPrefix+"attempt-crash"), dirPerm); err != nil {
		t.Fatal(err)
	}

	reopened, err := stOpenRun(s)
	if err != nil {
		t.Fatal(err)
	}
	if listed, listErr := reopened.ListAttempts("debate"); listErr != nil || len(listed) != 0 {
		t.Fatalf("orphan listed: %v, %v", listed, listErr)
	}
	stReserve(t, reopened, "debate", 1, turn, TurnParticipant, "alice", 1, 1, "sent")
	got, err := reopened.ListAttempts("debate")
	if err != nil || len(got) != 1 || got[0].Request.Message != "sent" {
		t.Fatalf("after resend: %+v, %v", got, err)
	}
}

// A reserved attempt is written again only as the resend (Resent) of the
// turn's newest attempt while it has no reply; its request.json is
// rewritten and it is reserved again.
func TestStoreResendAttempt(t *testing.T) {
	s := stNewStore(t)
	stLaunch(t, s, stConfig())
	turn := turnID(1, "alice")
	stReserve(t, s, "debate", 1, turn, TurnParticipant, "alice", 1, 1, "first")
	resend := &AttemptRequest{Layer: "debate", Round: 1, Turn: turn, Attempt: 1, Kind: TurnParticipant, Participant: "alice", ThroughSeq: 1, Message: "first"}

	if err := s.WriteAttemptRequest(resend); !errors.Is(err, os.ErrExist) {
		t.Fatalf("rewrite without Resent: %v, want ErrExist", err)
	}
	resend.Resent = true
	if err := s.WriteAttemptRequest(resend); err != nil {
		t.Fatalf("resend: %v", err)
	}
	if _, err := nextAppend(s, &Commit{
		Kind: CommitAttempt, Layer: "debate", Round: 1, Turn: turn, TurnKind: TurnParticipant,
		Participant: "alice", Attempt: 1, ThroughSeq: 1,
	}); err != nil {
		t.Fatalf("reserve the resend: %v", err)
	}
	got, err := stReopen(s).ListAttempts("debate")
	if err != nil || len(got) != 1 || !got[0].Request.Resent || got[0].Reply != nil {
		t.Fatalf("after resend: %+v, %v", got, err)
	}
	if _, _, err = verify(s); err != nil {
		t.Fatalf("verify after a resend: %v", err)
	}

	stReply(t, s, "debate", turn, 1, "x", []string{"bad"})

	// An attempt rewritten as a resend whose reply then arrived is not
	// reserved again.
	bobTurn := turnID(1, "bob")
	stReserve(t, s, "debate", 1, bobTurn, TurnParticipant, "bob", 1, 1, "bob first")
	bobResend := &AttemptRequest{Layer: "debate", Round: 1, Turn: bobTurn, Attempt: 1, Kind: TurnParticipant, Participant: "bob", ThroughSeq: 1, Message: "bob first", Resent: true}
	if err = s.WriteAttemptRequest(bobResend); err != nil {
		t.Fatalf("bob resend: %v", err)
	}
	stReply(t, s, "debate", bobTurn, 1, "x", []string{"bad"})
	if _, err = nextAppend(s, &Commit{
		Kind: CommitAttempt, Layer: "debate", Round: 1, Turn: bobTurn, TurnKind: TurnParticipant,
		Participant: "bob", Attempt: 1, ThroughSeq: 1,
	}); err == nil || !strings.Contains(err.Error(), "it has a reply") {
		t.Errorf("reserving an answered attempt again: %v, want refused", err)
	}

	// A repeated reservation whose request is not marked resent is corrupt.
	rel := attemptRel("debate", turn, 1) + "/" + fileRequest
	data, err := os.ReadFile(s.Path(rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(rel), []byte(strings.Replace(string(data), `"resent": true,`, "", 1)), filePerm); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verify(stReopen(s)); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "without being resent") {
		t.Errorf("verify of an unmarked repeat: %v, want ErrCorrupt", err)
	}
	if err := os.WriteFile(s.Path(rel), data, filePerm); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteAttemptRequest(resend); !errors.Is(err, os.ErrExist) {
		t.Errorf("resend of an answered attempt: %v, want ErrExist", err)
	}
	stReserve(t, s, "debate", 1, turn, TurnParticipant, "alice", 2, 1, "second")
	earlier := *resend
	if err := s.WriteAttemptRequest(&earlier); !errors.Is(err, os.ErrExist) {
		t.Errorf("resend of an earlier attempt: %v, want ErrExist", err)
	}
}

// An attempt is resent at most once: the store refuses to write or
// reserve it a third time, ListAttempts reports the resend as used, and
// replay refuses a log that reserves it a third time.
func TestStoreSecondResendRefused(t *testing.T) {
	s := stNewStore(t)
	stLaunch(t, s, stConfig())
	turn := turnID(1, "alice")
	stReserve(t, s, "debate", 1, turn, TurnParticipant, "alice", 1, 1, "first")
	resend := &AttemptRequest{Layer: "debate", Round: 1, Turn: turn, Attempt: 1, Kind: TurnParticipant, Participant: "alice", ThroughSeq: 1, Message: "first", Resent: true}
	reserve := &Commit{
		Kind: CommitAttempt, Layer: "debate", Round: 1, Turn: turn, TurnKind: TurnParticipant,
		Participant: "alice", Attempt: 1, ThroughSeq: 1,
	}
	got, err := s.ListAttempts("debate")
	if err != nil || len(got) != 1 || got[0].ResendUsed {
		t.Fatalf("before the resend: %+v, %v", got, err)
	}
	if err = s.WriteAttemptRequest(resend); err != nil {
		t.Fatalf("resend: %v", err)
	}
	if _, err = nextAppend(s, reserve); err != nil {
		t.Fatalf("reserve the resend: %v", err)
	}
	got, err = stReopen(s).ListAttempts("debate")
	if err != nil || len(got) != 1 || !got[0].ResendUsed {
		t.Fatalf("after the resend: %+v, %v", got, err)
	}

	if err = s.WriteAttemptRequest(resend); !errors.Is(err, os.ErrExist) || !strings.Contains(err.Error(), "already resent once") {
		t.Errorf("second resend request: %v, want refused", err)
	}
	if _, err = nextAppend(s, reserve); !errors.Is(err, os.ErrExist) || !strings.Contains(err.Error(), "already resent once") {
		t.Errorf("second resend reservation: %v, want refused", err)
	}
	if _, err = stReopen(s).ListAttempts("debate"); err != nil {
		t.Fatalf("the refused reservation damaged the log: %v", err)
	}

	cfg, snap, err := verify(s)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	commits, err := s.ReadCommits()
	if err != nil {
		t.Fatal(err)
	}
	third := commits[len(commits)-1]
	third.Seq++
	if _, err = replay(cfg, snap, append(commits, third)); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "already resent once") {
		t.Errorf("replay of a third reservation: %v, want ErrCorrupt", err)
	}
}

func TestStoreListAttemptsCorrupt(t *testing.T) {
	tests := []struct {
		name   string
		damage func(t *testing.T, dir string)
	}{
		{"request missing", func(t *testing.T, dir string) {
			t.Helper()
			if err := os.Remove(filepath.Join(dir, fileRequest)); err != nil {
				t.Fatal(err)
			}
		}},
		{"request truncated", func(t *testing.T, dir string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, fileRequest), []byte(`{"layer":`), filePerm); err != nil {
				t.Fatal(err)
			}
		}},
		{"request names another attempt", func(t *testing.T, dir string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, fileRequest), []byte(`{"layer":"debate","turn":"r001-bob","attempt":1}`), filePerm); err != nil {
				t.Fatal(err)
			}
		}},
		{"reply truncated", func(t *testing.T, dir string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, fileReply), []byte(`{"outcome`), filePerm); err != nil {
				t.Fatal(err)
			}
		}},
		{"attempt directory gone", func(t *testing.T, dir string) {
			t.Helper()
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stNewStore(t)
			stLaunch(t, s, stConfig())
			turn := turnID(1, "alice")
			stReserve(t, s, "debate", 1, turn, TurnParticipant, "alice", 1, 1, "m")
			tt.damage(t, s.Path(attemptRel("debate", turn, 1)))
			if _, err := s.ListAttempts("debate"); !errors.Is(err, ErrCorrupt) {
				t.Errorf("ListAttempts: %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestStoreWriteOutput(t *testing.T) {
	s := stNewStore(t)
	stLaunch(t, s, stConfig())
	turn := turnID(1, "alice")

	out := &OutputRecord{OutputID: uuid.NewString(), LayerID: "debate", Round: 1, ParticipantID: "alice", Format: FormatJSON, Turn: turn, Attempt: 1}
	if err := s.WriteOutput(out, []byte(`{}`), nil); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("output for an unreserved attempt: %v, want ErrInvalidState", err)
	}
	stReserve(t, s, "debate", 1, turn, TurnParticipant, "alice", 1, 1, "m")
	stReply(t, s, "debate", turn, 1, `{"claim":"c","secret":"s"}`, nil)

	// A first write that crashed before its CommitTurn is replaced by the
	// adopting controller's write.
	if err := s.WriteOutput(out, []byte(`{"stale":true}`), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	full, published := []byte(`{"claim":"c","secret":"s"}`), []byte(`{"claim":"c"}`)
	if err := s.WriteOutput(out, full, published); err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}
	dir := "layers/debate/calls/" + turn + "/1/"
	if out.ContentFile != dir+"output.json" || out.PublishedFile != dir+"published.json" ||
		out.Digest != digest(full) || out.PublishedDigest != digest(published) {
		t.Errorf("record %+v", out)
	}
	if got, err := s.ReadFile(out.ContentFile); err != nil || string(got) != string(full) {
		t.Errorf("content %q, %v", got, err)
	}
	if got, err := s.ReadFile(out.PublishedFile); err != nil || string(got) != string(published) {
		t.Errorf("published %q, %v", got, err)
	}
	if _, err := nextAppend(s, &Commit{Kind: CommitTurn, Layer: "debate", Round: 1, Turn: turn, Output: out}); err != nil {
		t.Fatal(err)
	}
	// One output per work ID.
	if err := s.WriteOutput(out, full, published); !errors.Is(err, os.ErrExist) {
		t.Errorf("WriteOutput after the turn committed: %v, want os.ErrExist", err)
	}

	// Without a projection the published file is the content file.
	md := turnID(1, "bob")
	stReserve(t, s, "summary", 1, md, TurnParticipant, "bob", 1, 2, "m")
	mdOut := &OutputRecord{LayerID: "summary", Round: 1, ParticipantID: "bob", Format: FormatMarkdown, Turn: md, Attempt: 1}
	if err := s.WriteOutput(mdOut, []byte("# Summary\n"), nil); err != nil {
		t.Fatal(err)
	}
	if mdOut.PublishedFile != mdOut.ContentFile || !strings.HasSuffix(mdOut.ContentFile, "/output.md") ||
		mdOut.PublishedDigest != mdOut.Digest {
		t.Errorf("markdown record %+v", mdOut)
	}
}

func TestStoreAppendCommit(t *testing.T) {
	s := stNewStore(t)
	if got, err := s.ReadCommits(); err != nil || len(got) != 0 {
		t.Fatalf("empty log = %v, %v", got, err)
	}
	// checkCommit needs the snapshot: nothing commits before it is written.
	if _, err := nextAppend(s, &Commit{Kind: CommitLaunched}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("AppendCommit before the snapshot: %v, want ErrInvalidState", err)
	}
	before := time.Now().UTC()
	stLaunch(t, s, stConfig())
	commits0, err := s.ReadCommits()
	if err != nil || len(commits0) != 1 || commits0[0].Seq != 1 || commits0[0].At.Before(before) {
		t.Fatalf("launched commit %+v, %v", commits0, err)
	}
	if _, err = os.Stat(s.Path("commits/00000001.json")); err != nil {
		t.Errorf("commit file: %v", err)
	}
	seq, err := nextAppend(s, &Commit{Kind: CommitLayerStarted, Layer: "debate"})
	if err != nil || seq != 2 {
		t.Errorf("second seq %d, %v", seq, err)
	}

	// A second store on the same directory (a bypassed lock) cannot
	// overwrite a commit: its index is stale and the exclusive write fails.
	stale := stReopen(s)
	if err = stale.loadIndexLocked(); err != nil {
		t.Fatal(err)
	}
	if _, err = nextAppend(s, &Commit{Kind: CommitPauseRequested}); err != nil {
		t.Fatal(err)
	}
	if err = stale.AppendCommit(3, &Commit{Kind: CommitPaused}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("stale writer: %v, want os.ErrExist", err)
	}
	// The failure dropped its index, so the seq it expected no longer
	// matches the log, and it continues from the real end.
	if err = stale.AppendCommit(3, &Commit{Kind: CommitPaused}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("stale writer at a taken seq: %v, want ErrInvalidState", err)
	}
	if err = stale.AppendCommit(4, &Commit{Kind: CommitPaused}); err != nil {
		t.Errorf("stale writer after reload: %v", err)
	}
	commits, err := s.ReadCommits()
	if err != nil || len(commits) != 4 || commits[2].Kind != CommitPauseRequested {
		t.Errorf("log %+v, %v", commits, err)
	}

	// A seq that does not follow the log is refused before anything is
	// written, whether it is ahead of the log or behind it.
	fresh := stReopen(s)
	for _, seq := range []int{0, 4, 6, 9} {
		c := &Commit{Kind: CommitResumed}
		if err := fresh.AppendCommit(seq, c); !errors.Is(err, ErrInvalidState) || !strings.Contains(err.Error(), "expected at seq") {
			t.Errorf("AppendCommit at seq %d: %v, want ErrInvalidState", seq, err)
		}
		if c.Seq != 0 {
			t.Errorf("refused commit got seq %d", c.Seq)
		}
		if _, err := os.Stat(s.Path(commitRel(seq))); seq > 4 && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("refused commit at seq %d was written: %v", seq, err)
		}
	}
}

// The store refuses every commit that breaks the log's invariants
// (checkCommit), and replay refuses the same commits, so the store never
// writes a log replay would reject.
func TestStoreAppendCommitInvariants(t *testing.T) {
	s := stNewStore(t)
	cfg := stConfig()
	snap := stLaunch(t, s, cfg)
	turn := turnID(1, "alice")
	stReserve(t, s, "debate", 1, turn, TurnParticipant, "alice", 1, 1, "m")
	out := &OutputRecord{LayerID: "debate", Round: 1, ParticipantID: "alice", Format: FormatJSON, Turn: turn, Attempt: 1}
	if err := s.WriteOutput(out, []byte(`{"claim":"c"}`), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := nextAppend(s, &Commit{Kind: CommitTurn, Layer: "debate", Round: 1, Turn: turn, Output: out}); err != nil {
		t.Fatal(err)
	}
	stReserve(t, s, "debate", 1, moderatorTurnID(1), TurnModerator, "mod", 1, 1, "m")
	guide := "look again"
	if _, err := nextAppend(s, &Commit{
		Kind: CommitModerated, Layer: "debate", Round: 1, Turn: moderatorTurnID(1),
		Decision: &Decision{Decision: DecisionGuide, Reason: "r", Guidance: &guide},
	}); err != nil {
		t.Fatal(err)
	}
	// Two attempts of one turn: only the newest may be reserved again (a
	// resend after a restart).
	alice2 := turnID(2, "alice")
	stReserve(t, s, "debate", 2, alice2, TurnParticipant, "alice", 1, 1, "m")
	stReserve(t, s, "debate", 2, alice2, TurnParticipant, "alice", 2, 1, "m")
	valid, err := s.ReadCommits()
	if err != nil {
		t.Fatal(err)
	}

	bob := turnID(1, "bob")
	bobOut := &OutputRecord{LayerID: "debate", Round: 1, ParticipantID: "bob", Turn: bob, Attempt: 1}
	attempt := func(layer string, round int, turn string, kind TurnKind, pid string, n int) Commit {
		return Commit{Kind: CommitAttempt, Layer: layer, Round: round, Turn: turn, TurnKind: kind, Participant: pid, Attempt: n}
	}
	withThrough := func(c Commit, through int) Commit {
		c.ThroughSeq = through
		return c
	}
	cont := &Decision{Decision: DecisionContinue}
	tests := []struct {
		name string
		c    Commit
		want string
		// storeOnly marks a check that needs the attempt directory, which
		// replay (working from the log alone) does not see.
		storeOnly bool
	}{
		{"no kind", Commit{}, "no kind", false},
		{"unknown kind", Commit{Kind: "rewind"}, "unknown kind", false},
		{"layer commit without layer", Commit{Kind: CommitLayerStarted}, "names no layer", false},
		{"layer not configured", attempt("../x", 1, turn, TurnParticipant, "alice", 2), "not in the configuration", false},
		{"attempt without request", attempt("debate", 1, bob, TurnParticipant, "bob", 1), "has no request", true},
		{"earlier attempt reserved again", attempt("debate", 2, alice2, TurnParticipant, "alice", 1), "exists", false},
		{"attempt reserved twice", withThrough(attempt("debate", 2, alice2, TurnParticipant, "alice", 2), 1), "exists", true},
		{"resend with another through_seq", withThrough(attempt("debate", 2, alice2, TurnParticipant, "alice", 2), 2), "reserved again with round 2 and through_seq 2, not 2 and 1", false},
		{"attempt for a committed turn", attempt("debate", 1, turn, TurnParticipant, "alice", 2), "already has a committed output", false},
		{"attempt not positive", attempt("debate", 1, bob, TurnParticipant, "bob", 0), "not positive", false},
		{"attempt turn of another participant", attempt("debate", 1, bob, TurnParticipant, "alice", 3), `is not "r001-alice"`, false},
		{"attempt by a non-member", attempt("debate", 1, turnID(1, "mod"), TurnParticipant, "mod", 1), `"mod" is not in layer`, false},
		{"attempt round above max_rounds", attempt("debate", 3, turnID(3, "bob"), TurnParticipant, "bob", 1), "outside 1..2", false},
		{"attempt round zero", attempt("debate", 0, turnID(0, "bob"), TurnParticipant, "bob", 1), "outside 1..2", false},
		{"attempt with unknown turn kind", attempt("debate", 1, bob, "", "bob", 1), "unknown turn kind", false},
		{"moderator attempt by a participant", attempt("debate", 2, moderatorTurnID(2), TurnModerator, "alice", 1), "is not the moderator", false},
		{"moderator attempt with a turn ID", attempt("debate", 2, turnID(2, "mod"), TurnModerator, "mod", 1), `is not "m002"`, false},
		{"moderator attempt without a moderator", attempt("summary", 1, moderatorTurnID(1), TurnModerator, "mod", 1), "is not the moderator", false},
		{"turn without output", Commit{Kind: CommitTurn, Layer: "debate", Round: 1, Turn: bob}, "needs an output", false},
		{"turn output of another turn", Commit{Kind: CommitTurn, Layer: "debate", Round: 1, Turn: bob, Output: out}, "needs an output", false},
		{"turn output of another round", Commit{Kind: CommitTurn, Layer: "debate", Round: 2, Turn: turn, Output: out}, "needs an output", false},
		{"second output for a turn", Commit{Kind: CommitTurn, Layer: "debate", Round: 1, Turn: turn, Output: out}, "exists", false},
		{"turn without a reserved attempt", Commit{Kind: CommitTurn, Layer: "debate", Round: 1, Turn: bob, Output: bobOut}, "attempt 1 is not reserved", false},
		{"moderated without decision", Commit{Kind: CommitModerated, Layer: "debate", Round: 2, Turn: moderatorTurnID(2)}, "no decision", false},
		{"second decision for a check", Commit{Kind: CommitModerated, Layer: "debate", Round: 1, Turn: moderatorTurnID(1), Decision: cont}, "exists", false},
		{"moderated without a reserved attempt", Commit{Kind: CommitModerated, Layer: "debate", Round: 2, Turn: moderatorTurnID(2), Decision: cont}, "no reserved attempt", false},
		{"moderated under a turn ID", Commit{Kind: CommitModerated, Layer: "debate", Round: 1, Turn: turnID(1, "mod"), Decision: cont}, `is not "m001"`, false},
		{"moderated without a moderator", Commit{Kind: CommitModerated, Layer: "summary", Round: 1, Turn: moderatorTurnID(1), Decision: cont}, "has no moderator", false},
		{"round_published before every turn", Commit{Kind: CommitRoundPublished, Layer: "debate", Round: 1}, "before bob's turn", false},
		{"round_published out of order", Commit{Kind: CommitRoundPublished, Layer: "debate", Round: 2}, "published after round 0", false},
		{"round_published above max_rounds", Commit{Kind: CommitRoundPublished, Layer: "debate", Round: 3}, "outside 1..2", false},
		{"round_published for per_turn", Commit{Kind: CommitRoundPublished, Layer: "summary", Round: 1}, "per_turn", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.c
			seq, appendErr := nextAppend(s, &c)
			if appendErr == nil || !strings.Contains(appendErr.Error(), tt.want) {
				t.Errorf("AppendCommit = %d, %v; want an error containing %q", seq, appendErr, tt.want)
			}
			if c.Seq != 0 {
				t.Errorf("rejected commit got seq %d", c.Seq)
			}
			if tt.storeOnly {
				return
			}
			bad := tt.c
			bad.Seq = len(valid) + 1
			_, replayErr := replay(cfg, snap, append(slices.Clone(valid), bad))
			if !errors.Is(replayErr, ErrCorrupt) || !strings.Contains(replayErr.Error(), tt.want) {
				t.Errorf("Replay: %v; want ErrCorrupt containing %q", replayErr, tt.want)
			}
		})
	}
	commits, err := s.ReadCommits()
	if err != nil || len(commits) != len(valid) {
		t.Errorf("log has %d commits (%v), want %d", len(commits), err, len(valid))
	}

	// The pass path: bob's reserved turn completes the round, which can
	// then be published, and replay accepts the whole log.
	stReserve(t, s, "debate", 1, bob, TurnParticipant, "bob", 1, 1, "m")
	if err = s.WriteOutput(bobOut, []byte(`{"claim":"b"}`), nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*Commit{
		{Kind: CommitTurn, Layer: "debate", Round: 1, Turn: bob, Output: bobOut},
		{Kind: CommitRoundPublished, Layer: "debate", Round: 1},
	} {
		if _, err = nextAppend(s, c); err != nil {
			t.Fatalf("AppendCommit(%s): %v", c.Kind, err)
		}
	}
	all, err := s.ReadCommits()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replay(cfg, snap, all); err != nil {
		t.Errorf("Replay of the accepted log: %v", err)
	}
}

// Concurrent appends at the same seq: exactly one lands.
func TestStoreAppendCommitConcurrent(t *testing.T) {
	s := stNewStore(t)
	stLaunch(t, s, stConfig())
	const n = 32
	var (
		wg sync.WaitGroup
		ok atomic.Int32
	)
	for range n {
		wg.Go(func() {
			err := s.AppendCommit(2, &Commit{Kind: CommitResumed})
			switch {
			case err == nil:
				ok.Add(1)
			case !errors.Is(err, ErrInvalidState):
				t.Errorf("losing append: %v, want ErrInvalidState", err)
			}
		})
	}
	wg.Wait()
	if got := ok.Load(); got != 1 {
		t.Errorf("%d appends at seq 2 landed, want 1", got)
	}
	commits, err := s.ReadCommits()
	if err != nil || len(commits) != 2 {
		t.Fatalf("log has %d commits, %v", len(commits), err)
	}
}

func TestStoreReadCommitsCorrupt(t *testing.T) {
	tests := []struct {
		name   string
		damage func(t *testing.T, s *forumStore)
	}{
		{"gap", func(t *testing.T, s *forumStore) {
			t.Helper()
			if err := os.Remove(s.Path(commitRel(2))); err != nil {
				t.Fatal(err)
			}
		}},
		{"truncated file", func(t *testing.T, s *forumStore) {
			t.Helper()
			if err := os.Truncate(s.Path(commitRel(3)), 5); err != nil {
				t.Fatal(err)
			}
		}},
		{"seq differs from name", func(t *testing.T, s *forumStore) {
			t.Helper()
			if err := os.WriteFile(s.Path(commitRel(3)), []byte(`{"seq":9,"kind":"paused"}`), filePerm); err != nil {
				t.Fatal(err)
			}
		}},
		{"stray file", func(t *testing.T, s *forumStore) {
			t.Helper()
			if err := os.WriteFile(s.Path("commits/notes.txt"), nil, filePerm); err != nil {
				t.Fatal(err)
			}
		}},
		{"first commit missing", func(t *testing.T, s *forumStore) {
			t.Helper()
			if err := os.Remove(s.Path(commitRel(1))); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stNewStore(t)
			stLaunch(t, s, stConfig())
			for range 2 {
				if _, err := nextAppend(s, &Commit{Kind: CommitResumed}); err != nil {
					t.Fatal(err)
				}
			}
			tt.damage(t, s)
			if _, err := s.ReadCommits(); !errors.Is(err, ErrCorrupt) {
				t.Errorf("ReadCommits: %v, want ErrCorrupt", err)
			}
			// The index is built from the same log, so writes fail too.
			fresh := stReopen(s)
			if err := fresh.AppendCommit(4, &Commit{Kind: CommitResumed}); !errors.Is(err, ErrCorrupt) {
				t.Errorf("AppendCommit on a corrupt log: %v, want ErrCorrupt", err)
			}
		})
	}
}

// A crash during writeFileDurable leaves only a temporary file: the
// target is either absent or complete. Readers ignore the leftovers and
// the next write succeeds.
func TestStoreCrashLeftovers(t *testing.T) {
	s := stNewStore(t)
	stLaunch(t, s, stConfig())
	for _, dir := range []string{dirCommits, "."} {
		if err := os.WriteFile(filepath.Join(s.Path(dir), forumfs.TempPrefix+"123"), []byte(`{"seq":2,"kind":"pau`), filePerm); err != nil {
			t.Fatal(err)
		}
	}
	commits, err := s.ReadCommits()
	if err != nil || len(commits) != 1 {
		t.Fatalf("ReadCommits with leftovers = %d, %v", len(commits), err)
	}
	fresh := stReopen(s)
	if err := fresh.AppendCommit(2, &Commit{Kind: CommitPauseRequested}); err != nil {
		t.Errorf("AppendCommit after a crash: %v", err)
	}
}

func TestStoreLock(t *testing.T) {
	s := stNewStore(t)
	other := stReopen(s)

	if err := s.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := s.Lock(); err != nil {
		t.Errorf("relock by the holder: %v", err)
	}
	pid, err := os.ReadFile(s.lockPath())
	if err != nil || strings.TrimSpace(string(pid)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock file holds %q, %v", pid, err)
	}
	stIsPerm(t, s.lockPath(), filePerm)
	if err := other.Lock(); !errors.Is(err, ErrLocked) {
		t.Errorf("second holder: %v, want ErrLocked", err)
	}
	s.Unlock()
	if _, err := os.Stat(s.lockPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("lock file after Unlock: %v", err)
	}
	s.Unlock() // no-op
	if err := other.Lock(); err != nil {
		t.Errorf("Lock after release: %v", err)
	}
	if err := s.Lock(); !errors.Is(err, ErrLocked) {
		t.Errorf("first store after handover: %v, want ErrLocked", err)
	}
	other.Unlock()
}

// The lock is exclusive across processes: a child process holds it while
// this one tries.
func TestStoreLockAcrossProcesses(t *testing.T) {
	s := stNewStore(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreLockHelperProcess$")
	cmd.Env = append(os.Environ(), "FORUM_STORE_LOCK_BASE="+s.base, "FORUM_STORE_LOCK_ID="+s.ID())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper said %q, %v", line, errors.Join(err, stdin.Close(), cmd.Wait()))
	}
	if err := s.Lock(); !errors.Is(err, ErrLocked) {
		t.Errorf("Lock while another process holds it: %v, want ErrLocked", err)
	}
	if err := s.Remove(); !errors.Is(err, ErrLocked) {
		t.Errorf("Remove while another process holds the lock: %v, want ErrLocked", err)
	}
	if _, err := os.Stat(s.Root()); err != nil {
		t.Errorf("root after a refused Remove: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	if err := s.Lock(); err != nil {
		t.Errorf("Lock after the other process released it: %v", err)
	}
	s.Unlock()
}

// TestStoreLockHelperProcess is the child of TestStoreLockAcrossProcesses;
// run directly it does nothing.
func TestStoreLockHelperProcess(t *testing.T) {
	base, id := os.Getenv("FORUM_STORE_LOCK_BASE"), os.Getenv("FORUM_STORE_LOCK_ID")
	if base == "" {
		return
	}
	s := newForumHandle(base, id)
	if err := s.Lock(); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	fmt.Println("locked")
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		fmt.Println("error:", err)
	}
	s.Unlock()
	os.Exit(0)
}

func TestStoreCleanupMarkers(t *testing.T) {
	s := stNewStore(t)
	if _, ok, err := s.Cleanup(cleanupAgents); ok || err != nil {
		t.Fatalf("absent marker: ok=%v err=%v", ok, err)
	}
	if err := s.SetCleanup(cleanupAgents, []byte(`["a"]`)); err != nil {
		t.Fatal(err)
	}
	stIsPerm(t, s.cleanupPath(cleanupAgents), filePerm)
	data, ok, err := s.Cleanup(cleanupAgents)
	if !ok || err != nil || string(data) != `["a"]` {
		t.Errorf("Cleanup = %q, %v, %v", data, ok, err)
	}
	if err := s.SetCleanup(cleanupAgents, []byte(`[]`)); err != nil {
		t.Errorf("rewrite marker: %v", err)
	}
	if err := s.ClearCleanup(cleanupAgents); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearCleanup(cleanupAgents); err != nil {
		t.Errorf("clearing a missing marker: %v", err)
	}
	if _, ok, err := s.Cleanup(cleanupAgents); ok || err != nil {
		t.Errorf("marker still present (%v)", err)
	}
	for _, bad := range []string{"", "..", "a/b"} {
		if err := s.SetCleanup(bad, nil); err == nil {
			t.Errorf("SetCleanup(%q) succeeded", bad)
		}
		if _, _, err := s.Cleanup(bad); err == nil {
			t.Errorf("Cleanup(%q) succeeded", bad)
		}
		if err := s.ClearCleanup(bad); err == nil {
			t.Errorf("ClearCleanup(%q) succeeded", bad)
		}
	}
}

func TestStoreRemove(t *testing.T) {
	s := stNewStore(t)
	base := s.base
	stLaunch(t, s, stConfig())
	keep := stNewRun(t, base)
	if err := keep.SetCleanup(cleanupAgents, []byte(`[]`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCleanup(cleanupAgents, []byte(`[]`)); err != nil {
		t.Fatal(err)
	}

	// Another holder blocks removal.
	holder := stReopen(s)
	if err := holder.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(); !errors.Is(err, ErrLocked) {
		t.Fatalf("Remove while locked elsewhere: %v, want ErrLocked", err)
	}
	holder.Unlock()

	// The holder itself may remove (Launch's failure path).
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, p := range []string{s.Dir(), filepath.Join(base, dirCleanup, s.ID()), s.cleanupPath(cleanupAgents), s.lockPath()} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s still exists after Remove (%v)", p, err)
		}
	}
	if ids, err := listForums(base); err != nil || len(ids) != 1 || ids[0] != keep.ID() {
		t.Errorf("ListForums after Remove = %v, %v", ids, err)
	}
	if _, ok, err := keep.Cleanup(cleanupAgents); !ok || err != nil {
		t.Errorf("Remove deleted another forum's marker (%v)", err)
	}
	if err := s.Lock(); err != nil { // the lock was released
		t.Errorf("Lock after Remove: %v", err)
	}
	s.Unlock()
}

// A crash after Remove's rename leaves a staged root; listStaged finds it
// and removeStaged finishes the job.
func TestStoreStagedRemoval(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none")
	if ids, err := listStaged(missing); err != nil || len(ids) != 0 {
		t.Fatalf("ListStaged(missing) = %v, %v", ids, err)
	}
	s := stNewStore(t)
	base := s.base
	stLaunch(t, s, stConfig())
	if err := s.SetCleanup(cleanupAgents, []byte(`["x"]`)); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(base, dirCleanup, s.ID())
	if err := os.Rename(s.Dir(), staged); err != nil {
		t.Fatal(err)
	}
	if ids, err := listForums(base); err != nil || len(ids) != 0 {
		t.Errorf("a staged forum is listed: %v, %v", ids, err)
	}
	ids, err := listStaged(base)
	if err != nil || len(ids) != 1 || ids[0] != s.ID() {
		t.Fatalf("ListStaged = %v, %v", ids, err)
	}

	holder := stReopen(s)
	if err := holder.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := removeStaged(base, s.ID()); !errors.Is(err, ErrLocked) {
		t.Errorf("RemoveStaged while locked: %v, want ErrLocked", err)
	}
	holder.Unlock()

	if err := removeStaged(base, s.ID()); err != nil {
		t.Fatalf("RemoveStaged: %v", err)
	}
	for _, p := range []string{staged, s.cleanupPath(cleanupAgents), s.lockPath()} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survives RemoveStaged (%v)", p, err)
		}
	}
	if ids, err := listStaged(base); err != nil || len(ids) != 0 {
		t.Errorf("ListStaged after RemoveStaged = %v, %v", ids, err)
	}
	if err := removeStaged(base, s.ID()); err != nil {
		t.Errorf("RemoveStaged with nothing staged: %v", err)
	}
	if err := removeStaged(base, "../x"); err == nil {
		t.Error("RemoveStaged accepted a path as forum ID")
	}
}

func TestStoreReadFileConfined(t *testing.T) {
	s := stNewStore(t)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), filePerm); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, s.Path("sources/link.txt")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../x", "/etc/passwd", "sources/link.txt", "sources/missing.txt"} {
		if data, err := s.ReadFile(rel); err == nil {
			t.Errorf("ReadFile(%q) = %q, want an error", rel, data)
		}
	}
	if s.Path("../x") != "" || s.Path("/abs") != "" {
		t.Error("Path accepted an escaping path")
	}
}

// Every directory the store creates is 0700 and every file 0600.
func TestStorePermissions(t *testing.T) {
	s := stNewStore(t)
	cfg := stConfig()
	stLaunch(t, s, cfg)
	if err := s.WriteLayerInputs(&LayerInputs{LayerID: "debate"}); err != nil {
		t.Fatal(err)
	}
	turn := turnID(1, "alice")
	stReserve(t, s, "debate", 1, turn, TurnParticipant, "alice", 1, 1, "m")
	stReply(t, s, "debate", turn, 1, `{"claim":"c"}`, nil)
	out := &OutputRecord{LayerID: "debate", Round: 1, ParticipantID: "alice", Format: FormatJSON, Turn: turn, Attempt: 1}
	if err := s.WriteOutput(out, []byte(`{"claim":"c"}`), []byte(`{"claim":"c"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	defer s.Unlock()
	if err := s.WriteState(&State{}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteResult(&Result{}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTranscript("entry\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCleanup(cleanupAgents, nil); err != nil {
		t.Fatal(err)
	}
	base := s.base
	files := 0
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == base {
			return nil
		}
		want := filePerm
		if d.IsDir() {
			want = dirPerm
		} else {
			files++
		}
		stIsPerm(t, p, want)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 14 {
		t.Errorf("walked only %d files", files)
	}
}

// Moderator work IDs have their own prefix: no participant ID, not even
// "moderator", yields one.
func TestWorkIDsCannotCollide(t *testing.T) {
	for _, pid := range []string{"moderator", "m001", "alice", "_", "-"} {
		for round := 1; round <= 3; round++ {
			if turnID(round, pid) == moderatorTurnID(round) {
				t.Errorf("TurnID(%d, %q) == ModeratorTurnID(%d)", round, pid, round)
			}
		}
	}
	if got := moderatorTurnID(1); got != "m001" || !validID(got) {
		t.Errorf("ModeratorTurnID(1) = %q", got)
	}
}

// An exclusive write publishes by hard link, so of several writers racing
// on one path exactly one wins and the others fail with os.ErrExist; the
// file holds the winner's bytes and no temporary file is left behind.
func TestStoreExclusiveWriteRace(t *testing.T) {
	s := stNewStore(t)
	const writers = 16
	for round := range 20 {
		rel := fmt.Sprintf("sources/race-%d.txt", round)
		var wg sync.WaitGroup
		errs := make([]error, writers)
		for i := range writers {
			wg.Go(func() { errs[i] = s.writeRel(rel, []byte(strconv.Itoa(i)), true) })
		}
		wg.Wait()
		winner := -1
		for i, err := range errs {
			switch {
			case err == nil && winner >= 0:
				t.Fatalf("round %d: writers %d and %d both succeeded", round, winner, i)
			case err == nil:
				winner = i
			case !errors.Is(err, os.ErrExist):
				t.Fatalf("round %d: writer %d: %v, want os.ErrExist", round, i, err)
			}
		}
		if winner < 0 {
			t.Fatalf("round %d: no writer succeeded", round)
		}
		if got, err := s.ReadFile(rel); err != nil || string(got) != strconv.Itoa(winner) {
			t.Fatalf("round %d: file holds %q (%v), want the winner's %d", round, got, err, winner)
		}
	}
	entries, err := os.ReadDir(s.Path(dirSources))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), forumfs.TempPrefix) {
			t.Errorf("leftover temporary file %s", e.Name())
		}
	}
}

// Writes follow no symbolic link below the root, whether it leads out of
// the root or back into it, and leave the link's target untouched.
func TestStoreWritesFollowNoSymlink(t *testing.T) {
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "victim")
	stWriteVictim := func(t *testing.T) {
		t.Helper()
		if err := os.WriteFile(outsideFile, []byte("untouched"), filePerm); err != nil {
			t.Fatal(err)
		}
	}
	stVictimUntouched := func(t *testing.T) {
		t.Helper()
		if got, err := os.ReadFile(outsideFile); err != nil || string(got) != "untouched" {
			t.Errorf("link target changed: %q, %v", got, err)
		}
		entries, err := os.ReadDir(outsideDir)
		if err != nil || len(entries) != 1 {
			t.Errorf("files created beside the link target: %v, %v", entries, err)
		}
	}

	t.Run("directory link out of the root", func(t *testing.T) {
		stWriteVictim(t)
		s := stNewStore(t)
		if err := os.Symlink(outsideDir, s.Path("layers/debate")); err != nil {
			t.Fatal(err)
		}
		if err := s.WriteLayerInputs(&LayerInputs{LayerID: "debate"}); err == nil {
			t.Error("WriteLayerInputs wrote through a linked directory")
		}
		stVictimUntouched(t)
	})
	t.Run("directory link within the root", func(t *testing.T) {
		s := stNewStore(t)
		if err := os.Symlink(s.Path(dirSources), s.Path("layers/debate")); err != nil {
			t.Fatal(err)
		}
		if err := s.WriteLayerInputs(&LayerInputs{LayerID: "debate"}); err == nil {
			t.Error("WriteLayerInputs wrote through a linked directory")
		}
		if entries, err := os.ReadDir(s.Path(dirSources)); err != nil || len(entries) != 0 {
			t.Errorf("files written into the link target: %v, %v", entries, err)
		}
	})
	t.Run("exclusive target is a link", func(t *testing.T) {
		stWriteVictim(t)
		s := stNewStore(t)
		if err := os.Symlink(outsideFile, s.Path(fileConfig)); err != nil {
			t.Fatal(err)
		}
		if err := s.WriteConfig([]byte("{}")); !errors.Is(err, os.ErrExist) {
			t.Errorf("WriteConfig over a link: %v, want os.ErrExist", err)
		}
		stVictimUntouched(t)
	})
	t.Run("replaced target is a link", func(t *testing.T) {
		stWriteVictim(t)
		s := stNewStore(t)
		stLaunch(t, s, stConfig())
		if err := s.Lock(); err != nil {
			t.Fatal(err)
		}
		defer s.Unlock()
		if err := os.Symlink(outsideFile, s.Path(fileState)); err != nil {
			t.Fatal(err)
		}
		if err := s.WriteState(&State{Status: StatusRunning}); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(s.Path(fileState))
		if err != nil || !fi.Mode().IsRegular() {
			t.Errorf("state.json is not a regular file after the write: %v, %v", fi, err)
		}
		stVictimUntouched(t)
	})
	t.Run("transcript is a link", func(t *testing.T) {
		stWriteVictim(t)
		s := stNewStore(t)
		if err := os.Symlink(outsideFile, s.Path(fileTranscript)); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendTranscript("entry\n"); err == nil {
			t.Error("AppendTranscript wrote through a link")
		}
		stVictimUntouched(t)
	})
	t.Run("record read through a link", func(t *testing.T) {
		s := stNewStore(t)
		inside := s.Path("sources/state-copy.json")
		if err := os.WriteFile(inside, []byte(`{"status":"running"}`), filePerm); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(inside, s.Path(fileState)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadState(); err == nil {
			t.Error("ReadState followed a link")
		}
	})
}

// Taking the lock sweeps the temporary files and directories a crashed
// writer left anywhere under the root, and nothing else.
func TestStoreLockSweepsTemporaries(t *testing.T) {
	s := stNewStore(t)
	stLaunch(t, s, stConfig())
	turnDir := s.Path("layers/debate/calls/" + turnID(1, "alice"))
	if err := os.MkdirAll(filepath.Join(turnDir, forumfs.TempPrefix+"attempt-x"), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(turnDir, forumfs.TempPrefix+"attempt-x", fileRequest), []byte("{}"), filePerm); err != nil {
		t.Fatal(err)
	}
	other, err := newForumHandle(s.base, s.id).CreateRun(2)
	if err != nil {
		t.Fatal(err)
	}
	untouched := filepath.Join(other.Path(dirCommits), forumfs.TempPrefix+"other-run")
	if err := os.WriteFile(untouched, []byte("partial"), filePerm); err != nil {
		t.Fatal(err)
	}
	leftovers := [...]string{
		filepath.Join(s.Root(), forumfs.TempPrefix+"1"),
		filepath.Join(s.Path(dirCommits), forumfs.TempPrefix+"2"),
		filepath.Join(s.Path(dirSources), forumfs.TempPrefix+"3"),
		filepath.Join(s.Dir(), forumfs.TempPrefix+"config"),
		filepath.Join(s.Dir(), dirRuns, forumfs.TempPrefix+"run-x"),
		filepath.Join(turnDir, forumfs.TempPrefix+"attempt-x"), // a directory, created above
	}
	for _, p := range leftovers[:5] {
		if err := os.WriteFile(p, []byte("partial"), filePerm); err != nil {
			t.Fatal(err)
		}
	}

	// Without the lock nothing is swept (another process may be writing).
	if _, err := loadState(s, stConfig(), &Snapshot{Layers: []string{"debate", "summary"}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range leftovers {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("%s swept without the lock: %v", p, err)
		}
	}

	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	defer s.Unlock()
	for _, p := range leftovers {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived the sweep: %v", p, err)
		}
	}
	for _, rel := range []string{fileConfig, fileSnapshot, fileParticipants, commitRel(1), "sources/notes.txt"} {
		if _, err := os.Lstat(s.Path(rel)); err != nil {
			t.Errorf("%s removed by the sweep: %v", rel, err)
		}
	}
	if _, err := os.Lstat(turnDir); err != nil {
		t.Errorf("turn directory removed by the sweep: %v", err)
	}
	// Another run is not walked when this one is locked; opening it does.
	if _, err := os.Lstat(untouched); err != nil {
		t.Errorf("another run was swept: %v", err)
	}
	if err := other.SweepRun(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(untouched); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("SweepRun left %s: %v", untouched, err)
	}
}

// ReplaceTranscript rewrites transcript.md in place: same file, new
// content, so `tail -f` keeps following it.
func TestStoreReplaceTranscript(t *testing.T) {
	s := stNewStore(t)
	if err := s.ReplaceTranscript([]byte("first\n")); err != nil {
		t.Fatalf("ReplaceTranscript on a missing file: %v", err)
	}
	if err := s.AppendTranscript("second\n"); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(s.Path(fileTranscript))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceTranscript([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(s.Path(fileTranscript))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("the transcript was replaced by another file")
	}
	if got, err := s.ReadFile(fileTranscript); err != nil || string(got) != "new\n" {
		t.Errorf("transcript = %q, %v", got, err)
	}
	if err := s.ReplaceTranscript(nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadFile(fileTranscript); err != nil || len(got) != 0 {
		t.Errorf("emptied transcript = %q, %v", got, err)
	}

	// A symbolic link in its place is refused, never followed.
	if err := os.Remove(s.Path(fileTranscript)); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Symlink(outside, s.Path(fileTranscript)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceTranscript([]byte("x")); err == nil {
		t.Error("ReplaceTranscript followed a symbolic link")
	}
	if _, err := os.Stat(outside); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a file was written through the link: %v", err)
	}
}
