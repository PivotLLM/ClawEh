// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"errors"
	"fmt"
	"os"
	"path"
)

// Seam (b): verification and replay (spec §8, rev 3 §8: "verify hashes,
// replay commits, and resume the first unfinished controller action"). The
// commit log is the truth; State is what Replay derives from it alone.

// Verify checks a forum directory before it is opened for execution:
//
//   - snapshot.json decodes and names this forum;
//   - forum.json decodes (Decode) and its SHA-256 equals
//     Snapshot.ConfigDigest;
//   - participants.json exists and decodes;
//   - every materialised source in Snapshot.Sources exists with the
//     recorded digest;
//   - the commit log has no gaps (ReadCommits);
//   - every layer with a CommitLayerStarted has an inputs.json that
//     decodes;
//   - every CommitAttempt has its request.json;
//   - every OutputRecord in a CommitTurn names an existing ContentFile
//     whose SHA-256 equals its Digest, and an existing PublishedFile.
//
// Any failure is ErrCorrupt wrapped with the detail: corrupt or missing
// committed artifacts fail recovery rather than regenerating history.
// Verify only reads. It returns the decoded configuration and snapshot so
// the caller does not read them twice.
func Verify(s *Store) (*Config, *Snapshot, error) {
	snap, err := s.ReadSnapshot()
	if err != nil {
		return nil, nil, corrupt("%s: %v", fileSnapshot, err)
	}
	if snap.ForumID != s.ID() {
		return nil, nil, corrupt("%s names forum %q", fileSnapshot, snap.ForumID)
	}
	raw, err := s.ReadConfig()
	if err != nil {
		return nil, nil, corrupt("%s: %v", fileConfig, err)
	}
	if got := digest(raw); got != snap.ConfigDigest {
		return nil, nil, corrupt("%s digest %s does not match the snapshot's %s", fileConfig, got, snap.ConfigDigest)
	}
	cfg, err := Decode(raw)
	if err != nil {
		return nil, nil, corrupt("%s: %v", fileConfig, err)
	}
	if _, err = s.ReadParticipants(); err != nil {
		return nil, nil, corrupt("%s: %v", fileParticipants, err)
	}
	for id, src := range snap.Sources {
		if err = verifyDigest(s, src.File, src.Digest); err != nil {
			return nil, nil, corrupt("source %q: %v", id, err)
		}
	}
	commits, err := s.ReadCommits()
	if err != nil {
		return nil, nil, err // already ErrCorrupt
	}
	for i := range commits {
		if err := verifyCommit(s, &commits[i]); err != nil {
			return nil, nil, corrupt("commit %d (%s): %v", commits[i].Seq, commits[i].Kind, err)
		}
	}
	return cfg, snap, nil
}

// verifyCommit checks the artifacts one commit references.
func verifyCommit(s *Store, c *Commit) error {
	switch c.Kind {
	case CommitLayerStarted:
		_, err := s.ReadLayerInputs(c.Layer)
		return err
	case CommitAttempt:
		rel := attemptRel(c.Layer, c.Turn, c.Attempt)
		if rel == "" {
			return errors.New("malformed attempt")
		}
		_, err := os.Stat(s.Path(path.Join(rel, fileRequest)))
		return err
	case CommitTurn:
		if c.Output == nil {
			return errors.New("no output record")
		}
		if err := verifyDigest(s, c.Output.ContentFile, c.Output.Digest); err != nil {
			return err
		}
		p := s.Path(c.Output.PublishedFile)
		if p == "" {
			return fmt.Errorf("published file %q escapes the forum root", c.Output.PublishedFile)
		}
		_, err := os.Stat(p)
		return err
	case CommitLaunched, CommitRoundPublished, CommitModerated, CommitLayerEnded, CommitPauseRequested,
		CommitPaused, CommitResumed, CommitCancelRequested, CommitEnded:
	}
	return nil
}

// verifyDigest checks that the root-relative rel exists and hashes to want.
func verifyDigest(s *Store, rel, want string) error {
	data, err := s.ReadFile(rel)
	if err != nil {
		return err
	}
	if got := digest(data); got != want {
		return fmt.Errorf("%s digest %s does not match the recorded %s", rel, got, want)
	}
	return nil
}

// corrupt wraps a formatted detail in ErrCorrupt.
func corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, args...))
}

// Replay derives State from the commits. It is deterministic: the same
// log always gives the same State (UpdatedAt is the last commit's time),
// and the result equals the state.json a crash-free run would have
// written, because the controller applies each commit with the same fold
// (replayApply over a state from replayInitialState).
//
// Derivation rules (replayApply):
//
//   - Status starts as StatusQueued and follows the commits: launched ->
//     running; pause_requested -> pausing; paused -> paused; resumed ->
//     running; cancel_requested -> cancelling; ended -> its Status with
//     Reason. A pausing or cancelling status at the end of the log means
//     the process died mid-transition; the controller completes it.
//   - LayerState: layer_started sets Started; attempt increments Calls and
//     sets Round; turn appends its Output; round_published increments
//     RoundsPublished (and so does the last turn of a round in a per_turn
//     layer); moderated appends a RoundDecision; layer_ended sets Ended
//     and EndReason.
//   - State.Calls is the number of attempt commits.
//   - ParticipantState: each attempt commit of TurnKind TurnParticipant
//     sets Briefed and Introduced[layer] for its Participant and raises
//     ThroughSeq to the commit's ThroughSeq.
//   - Seq is the last commit's sequence number.
//
// A commit that cannot follow the ones before it is ErrCorrupt: a seq out
// of order, a layer not in snap.Layers (or not in cfg), launched other
// than first, anything after ended, ended with a non-terminal status, a
// second output for one turn ID, a turn or moderated commit without its
// payload, round_published for a per_turn layer or out of round order.
func Replay(cfg *Config, snap *Snapshot, commits []Commit) (*State, error) {
	if cfg == nil || snap == nil {
		return nil, errors.New("replay: configuration and snapshot are required")
	}
	st := replayInitialState(snap)
	for i := range commits {
		if err := replayApply(cfg, snap, st, &commits[i]); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// replayInitialState is the State of a forum with an empty commit log:
// queued, every enabled layer present and not started, no participants
// briefed.
func replayInitialState(snap *Snapshot) *State {
	st := &State{
		Status:       StatusQueued,
		Layers:       make(map[string]*LayerState, len(snap.Layers)),
		Participants: map[string]*ParticipantState{},
	}
	for _, id := range snap.Layers {
		st.Layers[id] = &LayerState{Outputs: []OutputRecord{}}
	}
	return st
}

// replayApply folds one commit into st (see Replay for the rules). It is
// the single fold for the commit log: the controller applies each new
// commit with it and Replay is a loop over it, so the two cannot
// disagree. On error st may be partly updated and must be discarded.
func replayApply(cfg *Config, snap *Snapshot, st *State, c *Commit) error {
	if c.Seq != st.Seq+1 {
		return corrupt("commit seq %d follows %d", c.Seq, st.Seq)
	}
	if st.Status.Terminal() {
		return corrupt("commit %d (%s) follows the end of the run", c.Seq, c.Kind)
	}
	var ls *LayerState
	var layer Layer
	if c.Layer != "" {
		var ok bool
		if ls, ok = st.Layers[c.Layer]; !ok {
			return corrupt("commit %d names layer %q, which is not an enabled layer of the snapshot", c.Seq, c.Layer)
		}
		if layer, ok = cfg.Layer(c.Layer); !ok {
			return corrupt("commit %d names layer %q, which is not in the configuration", c.Seq, c.Layer)
		}
	}
	needLayer := func() error {
		if ls == nil {
			return corrupt("commit %d (%s) names no layer", c.Seq, c.Kind)
		}
		return nil
	}
	switch c.Kind {
	case CommitLaunched:
		if st.Status != StatusQueued {
			return corrupt("commit %d: launched while %s", c.Seq, st.Status)
		}
		st.Status = StatusRunning
	case CommitPauseRequested:
		st.Status = StatusPausing
	case CommitPaused:
		st.Status = StatusPaused
	case CommitResumed:
		st.Status = StatusRunning
	case CommitCancelRequested:
		st.Status = StatusCancelling
	case CommitEnded:
		if !c.Status.Terminal() {
			return corrupt("commit %d: ended with non-terminal status %q", c.Seq, c.Status)
		}
		st.Status, st.Reason = c.Status, c.Reason
	case CommitLayerStarted:
		if err := needLayer(); err != nil {
			return err
		}
		ls.Started = true
	case CommitAttempt:
		if err := needLayer(); err != nil {
			return err
		}
		ls.Calls++
		ls.Round = c.Round
		st.Calls++
		if c.TurnKind == TurnParticipant {
			if c.Participant == "" {
				return corrupt("commit %d: participant attempt names no participant", c.Seq)
			}
			ps := st.Participants[c.Participant]
			if ps == nil {
				ps = &ParticipantState{}
				st.Participants[c.Participant] = ps
			}
			ps.Briefed = true
			if ps.Introduced == nil {
				ps.Introduced = map[string]bool{}
			}
			ps.Introduced[c.Layer] = true
			ps.ThroughSeq = max(ps.ThroughSeq, c.ThroughSeq)
		}
	case CommitTurn:
		if err := needLayer(); err != nil {
			return err
		}
		if c.Output == nil {
			return corrupt("commit %d: turn %q has no output", c.Seq, c.Turn)
		}
		for _, o := range ls.Outputs {
			if o.Turn == c.Output.Turn {
				return corrupt("commit %d: turn %q already has an output", c.Seq, c.Output.Turn)
			}
		}
		ls.Outputs = append(ls.Outputs, *c.Output)
		if layer.Delivery == DeliveryPerTurn && roundComplete(layer, ls, c.Output.Round) {
			ls.RoundsPublished++
		}
	case CommitRoundPublished:
		if err := needLayer(); err != nil {
			return err
		}
		if layer.Delivery == DeliveryPerTurn {
			return corrupt("commit %d: round_published for per_turn layer %q", c.Seq, c.Layer)
		}
		if c.Round != ls.RoundsPublished+1 {
			return corrupt("commit %d: round %d published after %d rounds", c.Seq, c.Round, ls.RoundsPublished)
		}
		ls.RoundsPublished++
	case CommitModerated:
		if err := needLayer(); err != nil {
			return err
		}
		if c.Decision == nil {
			return corrupt("commit %d: moderation has no decision", c.Seq)
		}
		for _, d := range ls.Decisions {
			if d.Round == c.Round {
				return corrupt("commit %d: round %d already has a decision", c.Seq, c.Round)
			}
		}
		ls.Decisions = append(ls.Decisions, RoundDecision{Round: c.Round, Seq: c.Seq, Decision: *c.Decision})
	case CommitLayerEnded:
		if err := needLayer(); err != nil {
			return err
		}
		ls.Ended, ls.EndReason = true, c.Reason
	default:
		return corrupt("commit %d: unknown kind %q", c.Seq, c.Kind)
	}
	st.Seq = c.Seq
	st.UpdatedAt = c.At
	return nil
}

// roundComplete reports whether every participant of layer has a
// committed output for round.
func roundComplete(layer Layer, ls *LayerState, round int) bool {
	done := make(map[string]bool, len(layer.Participants))
	for _, o := range ls.Outputs {
		if o.Round == round {
			done[o.ParticipantID] = true
		}
	}
	for _, p := range layer.Participants {
		if !done[p] {
			return false
		}
	}
	return true
}

// LoadState returns the forum's current State: state.json when its Seq
// matches the last commit, otherwise a fresh Replay (which it writes back
// through WriteState so the next read is cheap). A state.json that is
// missing or does not decode is a stale cache, not corruption. It is what
// every reader (status, results) and the controller use; nothing reads
// state.json directly.
func LoadState(s *Store, cfg *Config, snap *Snapshot) (*State, error) {
	commits, err := s.ReadCommits()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	last := 0
	if n := len(commits); n > 0 {
		last = commits[n-1].Seq
	}
	if cached, readErr := s.ReadState(); readErr == nil && cached.Seq == last {
		return cached, nil
	}
	st, err := Replay(cfg, snap, commits)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	if err := s.WriteState(st); err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	return st, nil
}
