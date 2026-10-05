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
	"testing"
	"time"

	"github.com/google/uuid"
)

// Helpers in this file are prefixed st so they cannot collide with other
// seams' test helpers in the same package.

// stConfig is a two-layer forum: "debate" (after_round, two rounds,
// moderated, JSON output with share) and "summary" (per_turn, one round,
// markdown).
func stConfig() *Config {
	share := []string{"/claim"}
	return &Config{
		Version: ConfigVersion,
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

// stNewStore creates an empty forum store under a fresh base directory.
func stNewStore(t *testing.T) *Store {
	t.Helper()
	s, err := CreateStore(t.TempDir(), uuid.NewString())
	if err != nil {
		t.Fatalf("CreateStore: %v", err)
	}
	return s
}

// stLaunch writes everything Launch writes (forum.json, the source,
// participants.json, snapshot.json) and the launched commit.
func stLaunch(t *testing.T, s *Store, cfg *Config) *Snapshot {
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
		ForumID: s.ID(), Name: cfg.Name, LaunchedAt: launched, BaseDirectory: filepath.Dir(s.Root()),
		Deadline: launched.Add(10 * time.Minute), ConfigDigest: digest(raw), Seed: 7, Limits: cfg.Limits,
		Layers: []string{"debate", "summary"}, ResultLayers: []string{"summary"},
		Models:  map[string]string{"bob": "model-b"},
		Sources: map[string]SourceRecord{"notes": src},
	}
	if err := s.WriteSnapshot(snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	if _, err := s.AppendCommit(&Commit{Kind: CommitLaunched}); err != nil {
		t.Fatalf("AppendCommit(launched): %v", err)
	}
	return snap
}

// stReserve writes an attempt's request and commits its reservation.
func stReserve(t *testing.T, s *Store, layer string, round int, turn string, kind TurnKind, participant string, attempt, through int, message string) {
	t.Helper()
	req := &AttemptRequest{
		Layer: layer, Round: round, Turn: turn, Attempt: attempt, Kind: kind, Participant: participant,
		AgentID: participant, SentAt: time.Now().UTC(), WaitSeconds: 60, Repair: attempt > 1, ThroughSeq: through, Message: message,
	}
	if err := s.WriteAttemptRequest(req); err != nil {
		t.Fatalf("WriteAttemptRequest(%s/%s/%d): %v", layer, turn, attempt, err)
	}
	if _, err := s.AppendCommit(&Commit{
		Kind: CommitAttempt, Layer: layer, Round: round, Turn: turn, TurnKind: kind,
		Participant: participant, Attempt: attempt, ThroughSeq: through,
	}); err != nil {
		t.Fatalf("AppendCommit(attempt %s/%s/%d): %v", layer, turn, attempt, err)
	}
}

// stReply writes an attempt's reply.
func stReply(t *testing.T, s *Store, layer, turn string, attempt int, text string, issues []string) {
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
	base := filepath.Dir(s.Root())
	for _, d := range []string{
		s.Root(), filepath.Join(base, dirLocks), filepath.Join(base, dirCleanup),
		s.Path(dirSources), s.Path(dirLayers), s.Path(dirCommits),
	} {
		stIsPerm(t, d, dirPerm)
	}
	if s.ID() != filepath.Base(s.Root()) {
		t.Errorf("ID %q does not name the root %q", s.ID(), s.Root())
	}
}

func TestCreateStoreRejects(t *testing.T) {
	base := t.TempDir()
	id := uuid.NewString()
	if _, err := CreateStore(base, id); err != nil {
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
			if _, err := CreateStore(tt.base, tt.id); err == nil {
				t.Fatal("CreateStore succeeded, want an error")
			}
		})
	}
}

func TestOpenStore(t *testing.T) {
	s := stNewStore(t)
	base := filepath.Dir(s.Root())

	if _, err := OpenStore(base, s.ID()); !errors.Is(err, ErrNotFound) {
		t.Errorf("open without forum.json: %v, want ErrNotFound", err)
	}
	if err := s.WriteConfig([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	got, err := OpenStore(base, s.ID())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if got.Root() != s.Root() || got.ID() != s.ID() {
		t.Errorf("opened %s/%s, want %s/%s", got.Root(), got.ID(), s.Root(), s.ID())
	}

	for _, id := range []string{uuid.NewString(), "../" + s.ID(), "x"} {
		if _, err := OpenStore(base, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("OpenStore(%q): %v, want ErrNotFound", id, err)
		}
	}
	if _, err := OpenStore("relative", s.ID()); err == nil {
		t.Error("OpenStore with a relative base succeeded")
	}

	// A root that is a file, and a root missing commits/, are corrupt.
	fileRoot := uuid.NewString()
	if err := os.WriteFile(filepath.Join(base, fileRoot), nil, filePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(base, fileRoot); !errors.Is(err, ErrCorrupt) {
		t.Errorf("root is a file: %v, want ErrCorrupt", err)
	}
	if err := os.RemoveAll(s.Path(dirCommits)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(base, s.ID()); !errors.Is(err, ErrCorrupt) {
		t.Errorf("missing commits/: %v, want ErrCorrupt", err)
	}
}

func TestListForums(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing")
	if ids, err := ListForums(missing); err != nil || len(ids) != 0 {
		t.Fatalf("ListForums(missing) = %v, %v; want empty", ids, err)
	}
	base := t.TempDir()
	want := make([]string, 0, 3)
	for range 3 {
		s, err := CreateStore(base, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.WriteConfig([]byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		want = append(want, s.ID())
	}
	// Not listed: a root whose launch died before forum.json, a non-UUID
	// directory, and a plain file named like a forum.
	if _, err := CreateStore(base, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "notes"), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, uuid.NewString()), nil, filePerm); err != nil {
		t.Fatal(err)
	}
	got, err := ListForums(base)
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
		Participants: map[string]*ParticipantState{}, UpdatedAt: time.Now().UTC().Round(0),
	}
	for range 2 { // state.json is rewritten freely
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
	bobTurn, aliceTurn := TurnID(1, "bob"), TurnID(1, "alice")
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
	turn := TurnID(1, "alice")
	orphan := &AttemptRequest{Layer: "debate", Round: 1, Turn: turn, Attempt: 1, Kind: TurnParticipant, Participant: "alice", Message: "never sent"}
	if err := s.WriteAttemptRequest(orphan); err != nil {
		t.Fatal(err)
	}
	// The crash also left an attempt directory being assembled.
	if err := os.Mkdir(filepath.Join(s.Path("layers/debate/calls/"+turn), tmpPrefix+"attempt-crash"), dirPerm); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(filepath.Dir(s.Root()), s.ID())
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
			turn := TurnID(1, "alice")
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
	turn := TurnID(1, "alice")

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
	if out.ContentFile != dir+"output.json" || out.PublishedFile != dir+"published.json" || out.Digest != digest(full) {
		t.Errorf("record %+v", out)
	}
	if got, err := s.ReadFile(out.ContentFile); err != nil || string(got) != string(full) {
		t.Errorf("content %q, %v", got, err)
	}
	if got, err := s.ReadFile(out.PublishedFile); err != nil || string(got) != string(published) {
		t.Errorf("published %q, %v", got, err)
	}
	if _, err := s.AppendCommit(&Commit{Kind: CommitTurn, Layer: "debate", Round: 1, Turn: turn, Output: out}); err != nil {
		t.Fatal(err)
	}
	// One output per work ID.
	if err := s.WriteOutput(out, full, published); !errors.Is(err, os.ErrExist) {
		t.Errorf("WriteOutput after the turn committed: %v, want os.ErrExist", err)
	}

	// Without a projection the published file is the content file.
	md := TurnID(1, "bob")
	stReserve(t, s, "summary", 1, md, TurnParticipant, "bob", 1, 2, "m")
	mdOut := &OutputRecord{LayerID: "summary", Round: 1, ParticipantID: "bob", Format: FormatMarkdown, Turn: md, Attempt: 1}
	if err := s.WriteOutput(mdOut, []byte("# Summary\n"), nil); err != nil {
		t.Fatal(err)
	}
	if mdOut.PublishedFile != mdOut.ContentFile || !strings.HasSuffix(mdOut.ContentFile, "/output.md") {
		t.Errorf("markdown record %+v", mdOut)
	}
}

func TestStoreAppendCommit(t *testing.T) {
	s := stNewStore(t)
	if got, err := s.ReadCommits(); err != nil || len(got) != 0 {
		t.Fatalf("empty log = %v, %v", got, err)
	}
	before := time.Now().UTC()
	c := &Commit{Kind: CommitLaunched}
	seq, err := s.AppendCommit(c)
	if err != nil || seq != 1 || c.Seq != 1 || c.At.Before(before) {
		t.Fatalf("AppendCommit = %d, %v; commit %+v", seq, err, c)
	}
	if _, err = os.Stat(s.Path("commits/00000001.json")); err != nil {
		t.Errorf("commit file: %v", err)
	}
	if seq, err = s.AppendCommit(&Commit{Kind: CommitLayerStarted, Layer: "debate"}); err != nil || seq != 2 {
		t.Errorf("second seq %d, %v", seq, err)
	}

	// A second store on the same directory (a bypassed lock) cannot
	// overwrite a commit: its index is stale and the exclusive write fails.
	stale := &Store{base: filepath.Dir(s.Root()), id: s.ID(), root: s.Root()}
	if err = stale.loadIndexLocked(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendCommit(&Commit{Kind: CommitPauseRequested}); err != nil {
		t.Fatal(err)
	}
	if _, err = stale.AppendCommit(&Commit{Kind: CommitPaused}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("stale writer: %v, want os.ErrExist", err)
	}
	// The failure dropped its index, so it continues from the real end.
	if seq, err = stale.AppendCommit(&Commit{Kind: CommitPaused}); err != nil || seq != 4 {
		t.Errorf("stale writer after reload: %d, %v", seq, err)
	}
	commits, err := s.ReadCommits()
	if err != nil || len(commits) != 4 || commits[2].Kind != CommitPauseRequested {
		t.Errorf("log %+v, %v", commits, err)
	}
}

func TestStoreAppendCommitInvariants(t *testing.T) {
	s := stNewStore(t)
	stLaunch(t, s, stConfig())
	turn := TurnID(1, "alice")
	stReserve(t, s, "debate", 1, turn, TurnParticipant, "alice", 1, 1, "m")
	out := &OutputRecord{LayerID: "debate", Round: 1, ParticipantID: "alice", Format: FormatJSON, Turn: turn, Attempt: 1}
	if err := s.WriteOutput(out, []byte(`{"claim":"c"}`), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCommit(&Commit{Kind: CommitTurn, Layer: "debate", Round: 1, Turn: turn, Output: out}); err != nil {
		t.Fatal(err)
	}
	guide := "look again"
	if _, err := s.AppendCommit(&Commit{
		Kind: CommitModerated, Layer: "debate", Round: 1, Turn: ModeratorTurnID(1),
		Decision: &Decision{Decision: DecisionGuide, Reason: "r", Guidance: &guide},
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		c    Commit
	}{
		{"no kind", Commit{}},
		{"unknown kind", Commit{Kind: "rewind"}},
		{"attempt without request", Commit{Kind: CommitAttempt, Layer: "debate", Turn: TurnID(1, "bob"), Attempt: 1}},
		{"attempt reserved twice", Commit{Kind: CommitAttempt, Layer: "debate", Turn: turn, Attempt: 1}},
		{"attempt with bad IDs", Commit{Kind: CommitAttempt, Layer: "../x", Turn: turn, Attempt: 1}},
		{"turn without output", Commit{Kind: CommitTurn, Layer: "debate", Turn: turn}},
		{"turn output of another turn", Commit{Kind: CommitTurn, Layer: "debate", Turn: TurnID(1, "bob"), Output: out}},
		{"second output for a turn", Commit{Kind: CommitTurn, Layer: "debate", Turn: turn, Output: out}},
		{"moderated without decision", Commit{Kind: CommitModerated, Layer: "debate", Turn: ModeratorTurnID(2)}},
		{"second decision for a check", Commit{Kind: CommitModerated, Layer: "debate", Turn: ModeratorTurnID(1), Decision: &Decision{Decision: DecisionContinue}}},
	}
	last := 4
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.c
			if seq, err := s.AppendCommit(&c); err == nil {
				t.Errorf("AppendCommit succeeded with seq %d", seq)
			}
			if c.Seq != 0 {
				t.Errorf("rejected commit got seq %d", c.Seq)
			}
		})
	}
	commits, err := s.ReadCommits()
	if err != nil || len(commits) != last {
		t.Errorf("log has %d commits (%v), want %d", len(commits), err, last)
	}
}

func TestStoreAppendCommitConcurrent(t *testing.T) {
	s := stNewStore(t)
	const n = 32
	var wg sync.WaitGroup
	seqs := make(chan int, n)
	for range n {
		wg.Go(func() {
			seq, err := s.AppendCommit(&Commit{Kind: CommitResumed})
			if err != nil {
				t.Error(err)
			}
			seqs <- seq
		})
	}
	wg.Wait()
	close(seqs)
	seen := map[int]bool{}
	for seq := range seqs {
		if seen[seq] {
			t.Errorf("seq %d assigned twice", seq)
		}
		seen[seq] = true
	}
	commits, err := s.ReadCommits()
	if err != nil || len(commits) != n {
		t.Fatalf("log has %d commits, %v", len(commits), err)
	}
}

func TestStoreReadCommitsCorrupt(t *testing.T) {
	tests := []struct {
		name   string
		damage func(t *testing.T, s *Store)
	}{
		{"gap", func(t *testing.T, s *Store) {
			t.Helper()
			if err := os.Remove(s.Path(commitRel(2))); err != nil {
				t.Fatal(err)
			}
		}},
		{"truncated file", func(t *testing.T, s *Store) {
			t.Helper()
			if err := os.Truncate(s.Path(commitRel(3)), 5); err != nil {
				t.Fatal(err)
			}
		}},
		{"seq differs from name", func(t *testing.T, s *Store) {
			t.Helper()
			if err := os.WriteFile(s.Path(commitRel(3)), []byte(`{"seq":9,"kind":"paused"}`), filePerm); err != nil {
				t.Fatal(err)
			}
		}},
		{"stray file", func(t *testing.T, s *Store) {
			t.Helper()
			if err := os.WriteFile(s.Path("commits/notes.txt"), nil, filePerm); err != nil {
				t.Fatal(err)
			}
		}},
		{"first commit missing", func(t *testing.T, s *Store) {
			t.Helper()
			if err := os.Remove(s.Path(commitRel(1))); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stNewStore(t)
			for range 3 {
				if _, err := s.AppendCommit(&Commit{Kind: CommitResumed}); err != nil {
					t.Fatal(err)
				}
			}
			tt.damage(t, s)
			if _, err := s.ReadCommits(); !errors.Is(err, ErrCorrupt) {
				t.Errorf("ReadCommits: %v, want ErrCorrupt", err)
			}
			// The index is built from the same log, so writes fail too.
			fresh := &Store{base: filepath.Dir(s.Root()), id: s.ID(), root: s.Root()}
			if _, err := fresh.AppendCommit(&Commit{Kind: CommitResumed}); !errors.Is(err, ErrCorrupt) {
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
	if _, err := s.AppendCommit(&Commit{Kind: CommitLaunched}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{dirCommits, "."} {
		if err := os.WriteFile(filepath.Join(s.Path(dir), tmpPrefix+"123"), []byte(`{"seq":2,"kind":"pau`), filePerm); err != nil {
			t.Fatal(err)
		}
	}
	commits, err := s.ReadCommits()
	if err != nil || len(commits) != 1 {
		t.Fatalf("ReadCommits with leftovers = %d, %v", len(commits), err)
	}
	fresh := &Store{base: filepath.Dir(s.Root()), id: s.ID(), root: s.Root()}
	if seq, err := fresh.AppendCommit(&Commit{Kind: CommitPauseRequested}); err != nil || seq != 2 {
		t.Errorf("AppendCommit after a crash = %d, %v", seq, err)
	}
}

func TestStoreLock(t *testing.T) {
	s := stNewStore(t)
	other := &Store{base: filepath.Dir(s.Root()), id: s.ID(), root: s.Root()}

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
	cmd.Env = append(os.Environ(), "FORUM_STORE_LOCK_BASE="+filepath.Dir(s.Root()), "FORUM_STORE_LOCK_ID="+s.ID())
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
	s := &Store{base: base, id: id, root: filepath.Join(base, id)}
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
	if _, ok, err := s.Cleanup(CleanupAgents); ok || err != nil {
		t.Fatalf("absent marker: ok=%v err=%v", ok, err)
	}
	if err := s.SetCleanup(CleanupAgents, []byte(`["a"]`)); err != nil {
		t.Fatal(err)
	}
	stIsPerm(t, s.cleanupPath(CleanupAgents), filePerm)
	data, ok, err := s.Cleanup(CleanupAgents)
	if !ok || err != nil || string(data) != `["a"]` {
		t.Errorf("Cleanup = %q, %v, %v", data, ok, err)
	}
	if err := s.SetCleanup(CleanupAgents, []byte(`[]`)); err != nil {
		t.Errorf("rewrite marker: %v", err)
	}
	if err := s.ClearCleanup(CleanupAgents); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearCleanup(CleanupAgents); err != nil {
		t.Errorf("clearing a missing marker: %v", err)
	}
	if _, ok, err := s.Cleanup(CleanupAgents); ok || err != nil {
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
	base := filepath.Dir(s.Root())
	stLaunch(t, s, stConfig())
	keep, err := CreateStore(base, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if err := keep.WriteConfig([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := keep.SetCleanup(CleanupAgents, []byte(`[]`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCleanup(CleanupAgents, []byte(`[]`)); err != nil {
		t.Fatal(err)
	}

	// Another holder blocks removal.
	holder := &Store{base: base, id: s.ID(), root: s.Root()}
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
	for _, p := range []string{s.Root(), filepath.Join(base, dirCleanup, s.ID()), s.cleanupPath(CleanupAgents), s.lockPath()} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s still exists after Remove (%v)", p, err)
		}
	}
	if ids, err := ListForums(base); err != nil || len(ids) != 1 || ids[0] != keep.ID() {
		t.Errorf("ListForums after Remove = %v, %v", ids, err)
	}
	if _, ok, err := keep.Cleanup(CleanupAgents); !ok || err != nil {
		t.Errorf("Remove deleted another forum's marker (%v)", err)
	}
	if err := s.Lock(); err != nil { // the lock was released
		t.Errorf("Lock after Remove: %v", err)
	}
	s.Unlock()
}

// A crash after Remove's rename leaves a staged root; ListStaged finds it
// and RemoveStaged finishes the job.
func TestStoreStagedRemoval(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none")
	if ids, err := ListStaged(missing); err != nil || len(ids) != 0 {
		t.Fatalf("ListStaged(missing) = %v, %v", ids, err)
	}
	s := stNewStore(t)
	base := filepath.Dir(s.Root())
	stLaunch(t, s, stConfig())
	if err := s.SetCleanup(CleanupAgents, []byte(`["x"]`)); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(base, dirCleanup, s.ID())
	if err := os.Rename(s.Root(), staged); err != nil {
		t.Fatal(err)
	}
	if ids, err := ListForums(base); err != nil || len(ids) != 0 {
		t.Errorf("a staged forum is listed: %v, %v", ids, err)
	}
	ids, err := ListStaged(base)
	if err != nil || len(ids) != 1 || ids[0] != s.ID() {
		t.Fatalf("ListStaged = %v, %v", ids, err)
	}

	holder := &Store{base: base, id: s.ID(), root: s.Root()}
	if err := holder.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := RemoveStaged(base, s.ID()); !errors.Is(err, ErrLocked) {
		t.Errorf("RemoveStaged while locked: %v, want ErrLocked", err)
	}
	holder.Unlock()

	if err := RemoveStaged(base, s.ID()); err != nil {
		t.Fatalf("RemoveStaged: %v", err)
	}
	for _, p := range []string{staged, s.cleanupPath(CleanupAgents), s.lockPath()} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survives RemoveStaged (%v)", p, err)
		}
	}
	if ids, err := ListStaged(base); err != nil || len(ids) != 0 {
		t.Errorf("ListStaged after RemoveStaged = %v, %v", ids, err)
	}
	if err := RemoveStaged(base, s.ID()); err != nil {
		t.Errorf("RemoveStaged with nothing staged: %v", err)
	}
	if err := RemoveStaged(base, "../x"); err == nil {
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
	turn := TurnID(1, "alice")
	stReserve(t, s, "debate", 1, turn, TurnParticipant, "alice", 1, 1, "m")
	stReply(t, s, "debate", turn, 1, `{"claim":"c"}`, nil)
	out := &OutputRecord{LayerID: "debate", Round: 1, ParticipantID: "alice", Format: FormatJSON, Turn: turn, Attempt: 1}
	if err := s.WriteOutput(out, []byte(`{"claim":"c"}`), []byte(`{"claim":"c"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteState(&State{}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteResult(&Result{}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendTranscript("entry\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCleanup(CleanupAgents, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	defer s.Unlock()
	base := filepath.Dir(s.Root())
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
