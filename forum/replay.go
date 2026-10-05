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
	"slices"
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
//     whose SHA-256 equals its Digest, and an existing PublishedFile whose
//     SHA-256 equals its PublishedDigest.
//
// Every stat and read goes through the store's root-confined reader
// (Store.ReadFile, which follows no symbolic link). Any failure is
// ErrCorrupt wrapped with the detail: corrupt or missing committed
// artifacts fail recovery rather than regenerating history. Verify only
// reads. It returns the decoded configuration and snapshot so the caller
// does not read them twice.
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
		return s.statRegular(path.Join(rel, fileRequest))
	case CommitTurn:
		if c.Output == nil {
			return errors.New("no output record")
		}
		if err := verifyDigest(s, c.Output.ContentFile, c.Output.Digest); err != nil {
			return err
		}
		return verifyDigest(s, c.Output.PublishedFile, c.Output.PublishedDigest)
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
//   - What a participant was already sent is not part of State: the
//     controller derives it from the attempt commits themselves
//     (Controller.contact), the one source of truth.
//   - Seq is the last commit's sequence number.
//
// A commit that cannot follow the ones before it is ErrCorrupt: one that
// fails checkCommit (the check AppendCommit applies before writing, so
// the store never writes a log Replay refuses), a seq out of order,
// launched other than first, anything after ended, or ended with a
// non-terminal status.
func Replay(cfg *Config, snap *Snapshot, commits []Commit) (*State, error) {
	if cfg == nil || snap == nil {
		return nil, errors.New("replay: configuration and snapshot are required")
	}
	st := replayInitialState(snap)
	ix := newCommitIndex()
	for i := range commits {
		c := &commits[i]
		if err := checkCommit(cfg, snap, ix, c); err != nil {
			return nil, corrupt("commit %d: %v", c.Seq, err)
		}
		if err := replayApply(cfg, snap, st, c); err != nil {
			return nil, err
		}
		ix.add(c)
	}
	return st, nil
}

// checkCommit rejects a commit that cannot follow the log indexed by ix.
// It is the one structural check of the log: AppendCommit runs it before
// writing and Replay before folding, so the two cannot disagree. It needs
// nothing from disk. The rules:
//
//   - the kind is known; a layer, when named, is an enabled layer of the
//     snapshot and of the configuration, and every layer-scoped kind names
//     one;
//   - attempt: a positive attempt number not reserved before, for a turn
//     without a committed output. A participant attempt names a
//     participant of the layer and Turn == TurnID(Round, Participant); a
//     moderator attempt names the layer's moderator and Turn ==
//     ModeratorTurnID(Round);
//   - turn: an Output of this layer, round and turn, by a participant of
//     the layer, with Turn == TurnID(Round, Output.ParticipantID), whose
//     attempt (Output.Attempt) is reserved; no earlier output for the turn;
//   - moderated: a Decision, a layer with a moderator, Turn ==
//     ModeratorTurnID(Round), at least one reserved attempt for that turn
//     and no earlier decision for it;
//   - round_published: an after_round layer, the next round in order, and
//     every participant's turn of that round committed;
//   - every Round named by an attempt, turn, moderated or round_published
//     is between 1 and the layer's max_rounds.
func checkCommit(cfg *Config, snap *Snapshot, ix *commitIndex, c *Commit) error {
	var layer Layer
	if c.Layer != "" {
		var ok bool
		if layer, ok = cfg.Layer(c.Layer); !ok {
			return fmt.Errorf("layer %q is not in the configuration", c.Layer)
		}
		if !slices.Contains(snap.Layers, c.Layer) {
			return fmt.Errorf("layer %q is not an enabled layer of the snapshot", c.Layer)
		}
	}
	switch c.Kind {
	case CommitLaunched, CommitPauseRequested, CommitPaused, CommitResumed, CommitCancelRequested, CommitEnded:
		return nil
	case CommitLayerStarted, CommitLayerEnded, CommitAttempt, CommitTurn, CommitModerated, CommitRoundPublished:
		if c.Layer == "" {
			return fmt.Errorf("%s names no layer", c.Kind)
		}
	case "":
		return errors.New("no kind")
	default:
		return fmt.Errorf("unknown kind %q", c.Kind)
	}
	roundOK := func() error {
		if c.Round < 1 || c.Round > layer.MaxRounds {
			return fmt.Errorf("%s: round %d is outside 1..%d of layer %q", c.Kind, c.Round, layer.MaxRounds, c.Layer)
		}
		return nil
	}
	tk := turnKey{c.Layer, c.Turn}
	switch c.Kind {
	case CommitAttempt:
		if err := roundOK(); err != nil {
			return err
		}
		if c.Attempt < 1 {
			return fmt.Errorf("attempt %d is not positive", c.Attempt)
		}
		if err := checkWorkID(layer, c.TurnKind, c.Round, c.Participant, c.Turn); err != nil {
			return err
		}
		if ix.attempts[attemptKey{c.Layer, c.Turn, c.Attempt}] {
			return fmt.Errorf("attempt %s/%s/%d: %w", c.Layer, c.Turn, c.Attempt, os.ErrExist)
		}
		if ix.outputs[tk] {
			return fmt.Errorf("attempt for %s/%s, which already has a committed output", c.Layer, c.Turn)
		}
	case CommitTurn:
		o := c.Output
		if o == nil || o.LayerID != c.Layer || o.Turn != c.Turn || o.Round != c.Round {
			return fmt.Errorf("turn %q/%q (round %d) needs an output of that layer, turn and round", c.Layer, c.Turn, c.Round)
		}
		if err := roundOK(); err != nil {
			return err
		}
		if err := checkWorkID(layer, TurnParticipant, c.Round, o.ParticipantID, c.Turn); err != nil {
			return err
		}
		if !ix.attempts[attemptKey{c.Layer, c.Turn, o.Attempt}] {
			return fmt.Errorf("turn %s/%s: attempt %d is not reserved", c.Layer, c.Turn, o.Attempt)
		}
		if ix.outputs[tk] {
			return fmt.Errorf("turn %s/%s: %w", c.Layer, c.Turn, os.ErrExist)
		}
	case CommitModerated:
		if c.Decision == nil {
			return fmt.Errorf("moderation %q/%q has no decision", c.Layer, c.Turn)
		}
		if err := roundOK(); err != nil {
			return err
		}
		if layer.Moderator == nil {
			return fmt.Errorf("moderation of layer %q, which has no moderator", c.Layer)
		}
		if want := ModeratorTurnID(c.Round); c.Turn != want {
			return fmt.Errorf("moderation turn %q is not %q", c.Turn, want)
		}
		if !ix.reserved[tk] {
			return fmt.Errorf("moderation %s/%s has no reserved attempt", c.Layer, c.Turn)
		}
		if ix.outputs[tk] {
			return fmt.Errorf("moderation %s/%s: %w", c.Layer, c.Turn, os.ErrExist)
		}
	case CommitRoundPublished:
		if layer.Delivery == DeliveryPerTurn {
			return fmt.Errorf("round_published for per_turn layer %q", c.Layer)
		}
		if err := roundOK(); err != nil {
			return err
		}
		if last := ix.published[c.Layer]; c.Round != last+1 {
			return fmt.Errorf("round %d published after round %d", c.Round, last)
		}
		for _, pid := range layer.Participants {
			if !ix.outputs[turnKey{c.Layer, TurnID(c.Round, pid)}] {
				return fmt.Errorf("round %d published before %s's turn is committed", c.Round, pid)
			}
		}
	case CommitLaunched, CommitLayerStarted, CommitLayerEnded, CommitPauseRequested,
		CommitPaused, CommitResumed, CommitCancelRequested, CommitEnded:
	}
	return nil
}

// checkWorkID checks that a work ID belongs to its layer: a participant
// turn is TurnID(round, participant) for a participant of the layer, a
// moderator check is ModeratorTurnID(round) by the layer's moderator.
func checkWorkID(layer Layer, kind TurnKind, round int, participant, turn string) error {
	var want string
	switch kind {
	case TurnParticipant:
		if !slices.Contains(layer.Participants, participant) {
			return fmt.Errorf("participant %q is not in layer %q", participant, layer.ID)
		}
		want = TurnID(round, participant)
	case TurnModerator:
		if layer.Moderator == nil || layer.Moderator.Participant != participant {
			return fmt.Errorf("%q is not the moderator of layer %q", participant, layer.ID)
		}
		want = ModeratorTurnID(round)
	default:
		return fmt.Errorf("unknown turn kind %q", kind)
	}
	if turn != want {
		return fmt.Errorf("turn %q is not %q", turn, want)
	}
	return nil
}

// replayInitialState is the State of a forum with an empty commit log:
// queued, every enabled layer present and not started.
func replayInitialState(snap *Snapshot) *State {
	st := &State{
		Status: StatusQueued,
		Layers: make(map[string]*LayerState, len(snap.Layers)),
	}
	for _, id := range snap.Layers {
		st.Layers[id] = &LayerState{Outputs: []OutputRecord{}}
	}
	return st
}

// replayApply folds one commit into st (see Replay for the rules). It is
// the single fold for the commit log: the controller applies each new
// commit with it and Replay is a loop over it, so the two cannot
// disagree. It does not repeat checkCommit: the controller folds only
// commits AppendCommit accepted (after checkCommit), and Replay runs
// checkCommit itself. On error st may be partly updated and must be
// discarded.
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
	case CommitTurn:
		if err := needLayer(); err != nil {
			return err
		}
		if c.Output == nil {
			return corrupt("commit %d: turn %q has no output", c.Seq, c.Turn)
		}
		ls.Outputs = append(ls.Outputs, *c.Output)
		if layer.Delivery == DeliveryPerTurn && roundComplete(layer, ls, c.Output.Round) {
			ls.RoundsPublished++
		}
	case CommitRoundPublished:
		if err := needLayer(); err != nil {
			return err
		}
		ls.RoundsPublished++
	case CommitModerated:
		if err := needLayer(); err != nil {
			return err
		}
		if c.Decision == nil {
			return corrupt("commit %d: moderation has no decision", c.Seq)
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

// Two ways to obtain State, for two kinds of caller. Nothing else reads
// state.json.
//
//   - ReplayState is the controller's (Open): it always rebuilds State
//     from the commit log and rewrites state.json, so a damaged or
//     hand-edited cache can never steer a run. The caller holds the lock.
//   - LoadState is for read-only callers (status, results), which do not
//     hold the lock: it may use state.json as a cache and never writes.

// ReplayState rebuilds State from the commit log alone (Replay), ignoring
// state.json, and rewrites state.json with the result. Only the lock
// holder may call it: on an unlocked store it fails with ErrInvalidState
// (WriteState).
func ReplayState(s *Store, cfg *Config, snap *Snapshot) (*State, error) {
	commits, err := s.ReadCommits()
	if err != nil {
		return nil, fmt.Errorf("replay state: %w", err)
	}
	st, err := Replay(cfg, snap, commits)
	if err != nil {
		return nil, fmt.Errorf("replay state: %w", err)
	}
	if err := s.WriteState(st); err != nil {
		return nil, fmt.Errorf("replay state: %w", err)
	}
	return st, nil
}

// LoadState returns the forum's current State for a read-only caller:
// state.json when its Seq matches the last commit, otherwise a Replay of
// the log. It never writes state.json (only the lock holder does). A
// state.json that is missing or does not decode is a stale cache, not
// corruption.
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
	return st, nil
}
