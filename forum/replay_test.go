// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Helpers in this file are prefixed rp so they cannot collide with other
// seams' test helpers in the same package.

// rpRun drives a store the way the controller does: append a commit,
// fold it into the in-memory State with replayApply, rewrite state.json.
// states[k-1] is the State after commit k.
type rpRun struct {
	t       *testing.T
	s       *forumStore
	cfg     *Config
	snap    *Snapshot
	st      *State
	states  []string // JSON of the State after each commit
	public  []string // transcript entries, in publication order
	private []string // markers of material that must never be published
}

// rpJSON is the canonical comparison form of a State.
func rpJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func rpNewRun(t *testing.T) *rpRun {
	t.Helper()
	s := stNewStore(t)
	cfg := stConfig()
	snap := stLaunch(t, s, cfg)
	// The controller holds the lock; only the lock holder writes state.json.
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Unlock)
	r := &rpRun{
		t: t, s: s, cfg: cfg, snap: snap, st: replayInitialState(snap),
		private: []string{"PRIVATE-INSTRUCTIONS-ALICE", "PRIVATE-INSTRUCTIONS-BOB"},
	}
	commits, err := s.ReadCommits()
	if err != nil || len(commits) != 1 {
		t.Fatalf("launched commit: %v, %v", commits, err)
	}
	r.apply(&commits[0])
	return r
}

func (r *rpRun) apply(c *Commit) {
	r.t.Helper()
	if err := replayApply(r.cfg, r.snap, r.st, c); err != nil {
		r.t.Fatalf("replayApply(%s): %v", c.Kind, err)
	}
	if err := r.s.WriteState(r.st); err != nil {
		r.t.Fatal(err)
	}
	r.states = append(r.states, rpJSON(r.t, r.st))
}

func (r *rpRun) commit(c *Commit) int {
	r.t.Helper()
	seq, err := nextAppend(r.s, c)
	if err != nil {
		r.t.Fatalf("AppendCommit(%s): %v", c.Kind, err)
	}
	r.apply(c)
	return seq
}

func (r *rpRun) publish(entry string) {
	r.t.Helper()
	if err := r.s.AppendTranscript(entry); err != nil {
		r.t.Fatal(err)
	}
	r.public = append(r.public, entry)
}

func (r *rpRun) startLayer(layer string) {
	r.t.Helper()
	if err := r.s.WriteLayerInputs(&LayerInputs{LayerID: layer, Participants: map[string][]InputItem{}}); err != nil {
		r.t.Fatal(err)
	}
	r.commit(&Commit{Kind: CommitLayerStarted, Layer: layer})
}

// attempt reserves one attempt and records its reply.
func (r *rpRun) attempt(layer string, round int, turn string, kind TurnKind, pid string, n int, reply string, issues []string) {
	r.t.Helper()
	through := r.st.Seq
	msg := fmt.Sprintf("PRIVATE-MSG %s/%s/%d", layer, turn, n)
	r.private = append(r.private, msg)
	req := &AttemptRequest{
		Layer: layer, Round: round, Turn: turn, Attempt: n, Kind: kind, Participant: pid,
		AgentID: pid, ThroughSeq: through, Repair: n > 1, Message: msg,
	}
	if err := r.s.WriteAttemptRequest(req); err != nil {
		r.t.Fatal(err)
	}
	r.commit(&Commit{Kind: CommitAttempt, Layer: layer, Round: round, Turn: turn, TurnKind: kind, Participant: pid, Attempt: n, ThroughSeq: through})
	if err := r.s.WriteAttemptReply(layer, turn, n, &AttemptReply{Outcome: OutcomeOK, Text: reply, Issues: issues}); err != nil {
		r.t.Fatal(err)
	}
}

// turn runs a participant turn: rejected attempts first, then the
// accepted one, its output files and the CommitTurn. It returns the
// published text.
func (r *rpRun) turn(layer string, round int, pid string, rejected int) string {
	r.t.Helper()
	l, _ := r.cfg.Layer(layer)
	turn := turnID(round, pid)
	for n := 1; n <= rejected; n++ {
		bad := "PRIVATE-REJECTED " + turn
		r.private = append(r.private, bad)
		r.attempt(layer, round, turn, TurnParticipant, pid, n, bad, []string{"not valid"})
	}
	var full, published []byte
	if l.Output.Format == FormatJSON {
		secret := "PRIVATE-UNSHARED " + turn
		r.private = append(r.private, secret)
		full = fmt.Appendf(nil, `{"claim":"claim of %s","notes":%q}`, turn, secret)
		published = fmt.Appendf(nil, `{"claim":"claim of %s"}`, turn)
	} else {
		full = fmt.Appendf(nil, "summary of %s\n", turn)
	}
	r.attempt(layer, round, turn, TurnParticipant, pid, rejected+1, string(full), nil)
	out := &OutputRecord{
		OutputID: uuid.NewString(), LayerID: layer, Round: round, ParticipantID: pid,
		Format: l.Output.Format, Turn: turn, Attempt: rejected + 1,
	}
	if err := r.s.WriteOutput(out, full, published); err != nil {
		r.t.Fatal(err)
	}
	r.commit(&Commit{Kind: CommitTurn, Layer: layer, Round: round, Turn: turn, Output: out})
	if published != nil {
		return string(published)
	}
	return string(full)
}

// moderate runs the moderator check after round and commits a GUIDE
// decision whose assessment and directed message are private.
func (r *rpRun) moderate(layer string, round int) {
	r.t.Helper()
	turn := moderatorTurnID(round)
	guidance := "consider the cost"
	assessment, directed := "PRIVATE-ASSESSMENT", "PRIVATE-DIRECTED to bob"
	r.private = append(r.private, assessment, directed)
	d := &Decision{
		Decision: DecisionGuide, Reason: "a key issue remains", Guidance: &guidance,
		Assessment: json.RawMessage(fmt.Sprintf(`{"note":%q}`, assessment)),
		Directed:   []DirectedMessage{{To: "bob", Text: directed}},
	}
	reply, err := json.Marshal(d)
	if err != nil {
		r.t.Fatal(err)
	}
	r.attempt(layer, round, turn, TurnModerator, "mod", 1, string(reply), nil)
	r.commit(&Commit{Kind: CommitModerated, Layer: layer, Round: round, Turn: turn, Decision: d})
	r.publish(fmt.Sprintf("### Moderator after round %d\n\nGUIDE: %s\n\nGuidance: %s\n\n", round, d.Reason, guidance))
}

// rpFullRun runs the whole two-layer forum, with a rejected attempt, a
// moderator GUIDE, a pause and a resume, to completion.
func rpFullRun(t *testing.T) *rpRun {
	t.Helper()
	r := rpNewRun(t)
	r.startLayer("debate")
	for round := 1; round <= 2; round++ {
		a := r.turn("debate", round, "alice", 0)
		rejected := 0
		if round == 1 {
			rejected = 1
		}
		b := r.turn("debate", round, "bob", rejected)
		r.commit(&Commit{Kind: CommitRoundPublished, Layer: "debate", Round: round})
		r.publish(fmt.Sprintf("### Alice, round %d\n\n%s\n\n", round, a))
		r.publish(fmt.Sprintf("### Bob, round %d\n\n%s\n\n", round, b))
		if round == 1 {
			r.moderate("debate", 1)
		}
	}
	r.commit(&Commit{Kind: CommitLayerEnded, Layer: "debate", Reason: EndRoundLimit})
	r.commit(&Commit{Kind: CommitPauseRequested})
	r.commit(&Commit{Kind: CommitPaused})
	r.commit(&Commit{Kind: CommitResumed})
	r.startLayer("summary")
	for _, pid := range []string{"alice", "bob"} {
		text := r.turn("summary", 1, pid, 0)
		r.publish(fmt.Sprintf("### %s\n\n%s\n", pid, text))
	}
	r.commit(&Commit{Kind: CommitLayerEnded, Layer: "summary", Reason: EndRoundLimit})
	r.commit(&Commit{Kind: CommitEnded, Status: StatusCompleted, Reason: EndCompleted})
	return r
}

func TestReplayFullRun(t *testing.T) {
	r := rpFullRun(t)
	commits, err := r.s.ReadCommits()
	if err != nil {
		t.Fatal(err)
	}
	st, err := replay(r.cfg, r.snap, commits)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got, want := rpJSON(t, st), r.states[len(r.states)-1]; got != want {
		t.Fatalf("Replay differs from the incremental state:\n got %s\nwant %s", got, want)
	}

	if st.Status != StatusCompleted || st.Reason != EndCompleted || st.Seq != len(commits) {
		t.Errorf("status %s/%s seq %d", st.Status, st.Reason, st.Seq)
	}
	// debate: alice 1+1, bob 2+1, moderator 1 = 6; summary: 2.
	if st.Calls != 8 {
		t.Errorf("Calls = %d, want 8", st.Calls)
	}
	debate, summary := st.Layers["debate"], st.Layers["summary"]
	if debate.Calls != 6 || summary.Calls != 2 {
		t.Errorf("layer calls debate %d summary %d, want 6 and 2", debate.Calls, summary.Calls)
	}
	if !debate.Started || !debate.Ended || debate.EndReason != EndRoundLimit || debate.Round != 2 {
		t.Errorf("debate %+v", debate)
	}
	if debate.RoundsPublished != 2 || summary.RoundsPublished != 1 {
		t.Errorf("rounds published debate %d summary %d, want 2 and 1", debate.RoundsPublished, summary.RoundsPublished)
	}
	if len(debate.Outputs) != 4 || len(summary.Outputs) != 2 {
		t.Errorf("outputs debate %d summary %d", len(debate.Outputs), len(summary.Outputs))
	}
	if len(debate.Decisions) != 1 || debate.Decisions[0].Round != 1 || debate.Decisions[0].Decision.Decision != DecisionGuide {
		t.Errorf("decisions %+v", debate.Decisions)
	}
	if !st.UpdatedAt.Equal(commits[len(commits)-1].At) {
		t.Errorf("UpdatedAt %v, want the last commit's %v", st.UpdatedAt, commits[len(commits)-1].At)
	}
}

// replay is deterministic: the same log gives the same State, and every
// prefix of the log gives the State the controller had at that point.
func TestReplayDeterministicPrefixes(t *testing.T) {
	r := rpFullRun(t)
	commits, err := r.s.ReadCommits()
	if err != nil {
		t.Fatal(err)
	}
	for k := 0; k <= len(commits); k++ {
		a, errA := replay(r.cfg, r.snap, commits[:k])
		b, errB := replay(r.cfg, r.snap, commits[:k])
		if errA != nil || errB != nil {
			t.Fatalf("Replay(%d): %v, %v", k, errA, errB)
		}
		if rpJSON(t, a) != rpJSON(t, b) {
			t.Fatalf("Replay(%d) is not deterministic", k)
		}
		want := rpJSON(t, replayInitialState(r.snap))
		if k > 0 {
			want = r.states[k-1]
		}
		if got := rpJSON(t, a); got != want {
			t.Fatalf("Replay of %d commits:\n got %s\nwant %s", k, got, want)
		}
	}
}

func TestReplayStatusTransitions(t *testing.T) {
	tests := []struct {
		kinds  []CommitKind
		status Status
	}{
		{nil, StatusQueued},
		{[]CommitKind{CommitLaunched}, StatusRunning},
		{[]CommitKind{CommitLaunched, CommitPauseRequested}, StatusPausing},
		{[]CommitKind{CommitLaunched, CommitPauseRequested, CommitPaused}, StatusPaused},
		{[]CommitKind{CommitLaunched, CommitPauseRequested, CommitPaused, CommitResumed}, StatusRunning},
		{[]CommitKind{CommitLaunched, CommitCancelRequested}, StatusCancelling},
		{[]CommitKind{CommitLaunched, CommitPauseRequested, CommitPaused, CommitCancelRequested}, StatusCancelling},
	}
	cfg := stConfig()
	snap := &Snapshot{Layers: []string{"debate", "summary"}}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.kinds), func(t *testing.T) {
			commits := make([]Commit, len(tt.kinds))
			for i, k := range tt.kinds {
				commits[i] = Commit{Seq: i + 1, Kind: k}
			}
			st, err := replay(cfg, snap, commits)
			if err != nil {
				t.Fatal(err)
			}
			if st.Status != tt.status {
				t.Errorf("status %s, want %s", st.Status, tt.status)
			}
		})
	}
	st, err := replay(cfg, snap, []Commit{
		{Seq: 1, Kind: CommitLaunched},
		{Seq: 2, Kind: CommitCancelRequested},
		{Seq: 3, Kind: CommitEnded, Status: StatusCancelled, Reason: EndCancelled},
	})
	if err != nil || st.Status != StatusCancelled || st.Reason != EndCancelled {
		t.Errorf("ended: %+v, %v", st, err)
	}
	if _, err := replay(nil, snap, nil); err == nil {
		t.Error("Replay without a configuration succeeded")
	}
}

func TestReplayRejectsImpossibleLogs(t *testing.T) {
	cfg := stConfig()
	snap := &Snapshot{Layers: []string{"debate", "summary", "ghost"}}
	out := func(layer, turn string) *OutputRecord {
		return &OutputRecord{LayerID: layer, Round: 1, ParticipantID: "alice", Turn: turn}
	}
	cont := &Decision{Decision: DecisionContinue}
	launched := Commit{Kind: CommitLaunched}
	tests := []struct {
		name string
		log  []Commit
	}{
		{"seq gap", []Commit{launched, {Kind: CommitResumed, Seq: 3}}},
		{"layer not in snapshot", []Commit{launched, {Kind: CommitLayerStarted, Layer: "other"}}},
		{"layer not in configuration", []Commit{launched, {Kind: CommitLayerStarted, Layer: "ghost"}}},
		{"launched twice", []Commit{launched, launched}},
		{"commit after ended", []Commit{launched, {Kind: CommitEnded, Status: StatusFailed}, {Kind: CommitResumed}}},
		{"ended not terminal", []Commit{launched, {Kind: CommitEnded, Status: StatusPaused}}},
		{"layer commit without layer", []Commit{launched, {Kind: CommitLayerStarted}}},
		{"attempt without layer", []Commit{launched, {Kind: CommitAttempt}}},
		{"participant attempt without participant", []Commit{launched, {Kind: CommitAttempt, Layer: "debate", TurnKind: TurnParticipant}}},
		{"turn without output", []Commit{launched, {Kind: CommitTurn, Layer: "debate", Turn: "r001-alice"}}},
		{"second output for a turn", []Commit{
			launched,
			{Kind: CommitTurn, Layer: "debate", Turn: "r001-alice", Output: out("debate", "r001-alice")},
			{Kind: CommitTurn, Layer: "debate", Turn: "r001-alice", Output: out("debate", "r001-alice")},
		}},
		{"round_published for per_turn", []Commit{launched, {Kind: CommitRoundPublished, Layer: "summary", Round: 1}}},
		{"round_published out of order", []Commit{launched, {Kind: CommitRoundPublished, Layer: "debate", Round: 2}}},
		{"moderated without decision", []Commit{launched, {Kind: CommitModerated, Layer: "debate", Round: 1}}},
		{"second decision for a round", []Commit{
			launched,
			{Kind: CommitModerated, Layer: "debate", Round: 1, Decision: cont},
			{Kind: CommitModerated, Layer: "debate", Round: 1, Decision: cont},
		}},
		{"unknown kind", []Commit{launched, {Kind: "rewind"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := range tt.log {
				if tt.log[i].Seq == 0 {
					tt.log[i].Seq = i + 1
				}
			}
			if _, err := replay(cfg, snap, tt.log); !errors.Is(err, ErrCorrupt) {
				t.Errorf("Replay: %v, want ErrCorrupt", err)
			}
		})
	}
}

// A per_turn round counts as published when its last participant's turn
// commits, whatever order the turns land in.
func TestReplayPerTurnRounds(t *testing.T) {
	cfg := stConfig()
	snap := &Snapshot{Layers: []string{"summary"}}
	log := make([]Commit, 0, 4)
	log = append(log, Commit{Seq: 1, Kind: CommitLaunched}, Commit{Seq: 2, Kind: CommitLayerStarted, Layer: "summary"})
	for _, pid := range []string{"bob", "alice"} {
		turn := turnID(1, pid)
		log = append(log, Commit{
			Seq: len(log) + 1, Kind: CommitAttempt, Layer: "summary", Round: 1, Turn: turn,
			TurnKind: TurnParticipant, Participant: pid, Attempt: 1,
		})
	}
	for _, pid := range []string{"bob", "alice"} {
		turn := turnID(1, pid)
		log = append(log, Commit{
			Seq: len(log) + 1, Kind: CommitTurn, Layer: "summary", Round: 1, Turn: turn,
			Output: &OutputRecord{LayerID: "summary", Round: 1, ParticipantID: pid, Turn: turn, Attempt: 1},
		})
	}
	st, err := replay(cfg, snap, log[:5])
	if err != nil || st.Layers["summary"].RoundsPublished != 0 {
		t.Fatalf("after one turn: %+v, %v", st, err)
	}
	st, err = replay(cfg, snap, log)
	if err != nil || st.Layers["summary"].RoundsPublished != 1 {
		t.Fatalf("after both turns: %+v, %v", st.Layers["summary"], err)
	}
}

// loadState uses state.json only when it matches the log, and otherwise
// replays; it never writes state.json (read-only callers do not hold the
// lock).
func TestLoadStateCache(t *testing.T) {
	r := rpFullRun(t)
	last := r.st.Seq

	// A current cache is returned as is (the marker proves it was read).
	cached := *r.st
	cached.Reason = "from-cache"
	if err := r.s.WriteState(&cached); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(r.s, r.cfg, r.snap)
	if err != nil || st.Reason != "from-cache" {
		t.Fatalf("current cache not used: %v, %v", st.Reason, err)
	}

	for name, damage := range map[string]func() error{
		"stale":     func() error { cached.Seq = last - 1; return r.s.WriteState(&cached) },
		"missing":   func() error { return os.Remove(r.s.Path(fileState)) },
		"truncated": func() error { return os.WriteFile(r.s.Path(fileState), []byte(`{"status":`), filePerm) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := damage(); err != nil {
				t.Fatal(err)
			}
			st, err := loadState(r.s, r.cfg, r.snap)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := rpJSON(t, st), r.states[len(r.states)-1]; got != want {
				t.Errorf("LoadState:\n got %s\nwant %s", got, want)
			}
			before, beforeErr := os.ReadFile(r.s.Path(fileState))
			if _, err := loadState(r.s, r.cfg, r.snap); err != nil {
				t.Fatal(err)
			}
			after, afterErr := os.ReadFile(r.s.Path(fileState))
			if string(before) != string(after) || (beforeErr == nil) != (afterErr == nil) {
				t.Errorf("LoadState wrote state.json:\nbefore %q (%v)\nafter  %q (%v)", before, beforeErr, after, afterErr)
			}
		})
	}

	if err := os.Remove(r.s.Path(commitRel(2))); err != nil {
		t.Fatal(err)
	}
	if _, err := loadState(r.s, r.cfg, r.snap); !errors.Is(err, ErrCorrupt) {
		t.Errorf("LoadState over a log with a gap: %v, want ErrCorrupt", err)
	}
}

// replayState (the controller's path at openForum) never trusts state.json: it
// replays the log even when the cache claims to be current, rewrites
// state.json with the result, and refuses to run without the lock.
func TestReplayStateIgnoresTheCache(t *testing.T) {
	r := rpFullRun(t)
	want := r.states[len(r.states)-1]
	forged := *r.st
	forged.Reason = "forged"
	forged.Calls = 999
	if err := r.s.WriteState(&forged); err != nil {
		t.Fatal(err)
	}
	st, err := replayState(r.s, r.cfg, r.snap)
	if err != nil {
		t.Fatalf("ReplayState: %v", err)
	}
	if got := rpJSON(t, st); got != want {
		t.Errorf("ReplayState:\n got %s\nwant %s", got, want)
	}
	written, err := r.s.ReadState()
	if err != nil || rpJSON(t, written) != want {
		t.Errorf("state.json not rewritten: %v", err)
	}

	unlocked, err := stOpenRun(r.s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replayState(unlocked, r.cfg, r.snap); !errors.Is(err, ErrInvalidState) {
		t.Errorf("ReplayState without the lock: %v, want ErrInvalidState", err)
	}
	if err := unlocked.WriteState(st); !errors.Is(err, ErrInvalidState) {
		t.Errorf("WriteState without the lock: %v, want ErrInvalidState", err)
	}
}

// A crash can happen after any commit and before its state.json is
// rewritten, leaving whatever artifacts the next step had written. For
// every commit boundary k: cut the log after k, leave state.json at k-1
// and a temporary file in commits/, and check loadState recovers exactly
// the State the controller had after commit k, and verify accepts the
// directory.
func TestRecoveryAtEveryCommitBoundary(t *testing.T) {
	r := rpFullRun(t)
	n := len(r.states)
	for k := n; k >= 1; k-- {
		for seq := k + 1; seq <= n; seq++ {
			if err := os.Remove(r.s.Path(commitRel(seq))); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
		}
		if k == 1 {
			if err := os.Remove(r.s.Path(fileState)); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(r.s.Path(fileState), []byte(r.states[k-2]), filePerm); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.s.Path(dirCommits), TempPrefix+"crash"), []byte(`{"seq":`), filePerm); err != nil {
			t.Fatal(err)
		}

		s, err := stOpenRun(r.s)
		if err != nil {
			t.Fatalf("k=%d: OpenStore: %v", k, err)
		}
		cfg, snap, err := verify(s)
		if err != nil {
			t.Fatalf("k=%d: Verify: %v", k, err)
		}
		st, err := loadState(s, cfg, snap)
		if err != nil {
			t.Fatalf("k=%d: LoadState: %v", k, err)
		}
		if got := rpJSON(t, st); got != r.states[k-1] {
			t.Fatalf("k=%d: recovered\n %s\nwant\n %s", k, got, r.states[k-1])
		}
		// Every attempt reserved by the surviving log is listed; none after.
		reserved := 0
		for _, layer := range snap.Layers {
			got, err := s.ListAttempts(layer)
			if err != nil {
				t.Fatalf("k=%d: ListAttempts: %v", k, err)
			}
			reserved += len(got)
		}
		if reserved != st.Calls {
			t.Errorf("k=%d: %d attempts listed, %d reserved", k, reserved, st.Calls)
		}
	}
}

func TestVerifyAcceptsAFullRun(t *testing.T) {
	r := rpFullRun(t)
	cfg, snap, err := verify(r.s)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if snap.ForumID != r.s.ID() || cfg.Name != r.cfg.Name || len(cfg.Layers) != 2 {
		t.Errorf("Verify returned %+v / %+v", cfg, snap)
	}
}

// verify fails on every tampered or missing committed artifact, and never
// repairs or regenerates it.
func TestVerifyDetectsDamage(t *testing.T) {
	committedOutput := func(r *rpRun, layer string, published bool) string {
		o := r.st.Layers[layer].Outputs[0]
		if published {
			return o.PublishedFile
		}
		return o.ContentFile
	}
	firstRequest := func(r *rpRun) string {
		return attemptRel("debate", turnID(1, "alice"), 1) + "/" + fileRequest
	}
	tests := []struct {
		name   string
		target func(r *rpRun) string
		remove bool
	}{
		{"forum.json changed", func(*rpRun) string { return fileConfig }, false},
		{"snapshot.json missing", func(*rpRun) string { return fileSnapshot }, true},
		{"participants.json missing", func(*rpRun) string { return fileParticipants }, true},
		{"source changed", func(r *rpRun) string { return r.snap.Sources["notes"].File }, false},
		{"source missing", func(r *rpRun) string { return r.snap.Sources["notes"].File }, true},
		{"inputs.json missing", func(*rpRun) string { return "layers/debate/" + fileInputs }, true},
		{"committed request missing", firstRequest, true},
		{"committed output changed", func(r *rpRun) string { return committedOutput(r, "debate", false) }, false},
		{"committed output missing", func(r *rpRun) string { return committedOutput(r, "summary", false) }, true},
		{"published projection missing", func(r *rpRun) string { return committedOutput(r, "debate", true) }, true},
		{"published projection changed", func(r *rpRun) string { return committedOutput(r, "debate", true) }, false},
		{"commit missing", func(*rpRun) string { return commitRel(5) }, true},
		{"commit truncated", func(*rpRun) string { return commitRel(5) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := rpFullRun(t)
			target := r.s.Path(tt.target(r))
			if tt.remove {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			} else {
				data, err := os.ReadFile(target)
				if err != nil {
					t.Fatal(err)
				}
				data = data[:len(data)/2] // a torn write
				if err := os.WriteFile(target, data, filePerm); err != nil {
					t.Fatal(err)
				}
			}
			before, beforeErr := os.ReadFile(target)
			if _, _, err := verify(r.s); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Verify: %v, want ErrCorrupt", err)
			}
			after, afterErr := os.ReadFile(target)
			if string(before) != string(after) || (beforeErr == nil) != (afterErr == nil) {
				t.Error("Verify changed the damaged artifact")
			}
		})
	}

	// A committed artifact replaced by a symbolic link to identical content
	// is still damage: verify follows no link, inside the root or out.
	for _, published := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlinked artifact published=%v", published), func(t *testing.T) {
			r := rpFullRun(t)
			rel := committedOutput(r, "debate", published)
			target := r.s.Path(rel)
			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "copy")
			if err := os.WriteFile(outside, data, filePerm); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Fatal(err)
			}
			if _, _, err := verify(r.s); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Verify: %v, want ErrCorrupt", err)
			}
		})
	}

	t.Run("snapshot of another forum", func(t *testing.T) {
		r := rpFullRun(t)
		snap := *r.snap
		snap.ForumID = uuid.NewString()
		data, err := marshalRecord(&snap)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(r.s.Path(fileSnapshot), data, filePerm); err != nil {
			t.Fatal(err)
		}
		if _, _, err := verify(r.s); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "names forum") {
			t.Fatalf("Verify: %v, want ErrCorrupt naming the forum", err)
		}
	})
}

// verify fails on an undecodable configuration even when its digest
// matches (the configuration was accepted once, so this is corruption).
func TestVerifyUndecodableConfig(t *testing.T) {
	s := stNewStore(t)
	raw := []byte(`{"version":1,`)
	if err := s.WriteConfig(raw); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSnapshot(&Snapshot{ForumID: s.ID(), Run: s.RunNumber(), ConfigDigest: digest(raw)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verify(s); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Verify: %v, want ErrCorrupt", err)
	}
}
