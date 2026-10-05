// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

// Seam (b): verification and replay (spec §8, rev 3 §8: "verify hashes,
// replay commits, and resume the first unfinished controller action"). The
// commit log is the truth; State is what Replay derives from it alone.

// Verify checks a forum directory before it is opened for execution:
//
//   - forum.json decodes (Decode) and its SHA-256 equals
//     Snapshot.ConfigDigest;
//   - snapshot.json and participants.json exist and decode;
//   - every materialised source in Snapshot.Sources exists with the
//     recorded digest;
//   - the commit log has no gaps (ReadCommits);
//   - every layer with a CommitLayerStarted has an inputs.json;
//   - every OutputRecord in a CommitTurn names an existing ContentFile
//     whose SHA-256 equals its Digest, and an existing PublishedFile.
//
// Any failure is ErrCorrupt wrapped with the detail: corrupt or missing
// committed artifacts fail recovery rather than regenerating history. It
// returns the decoded configuration and snapshot so the caller does not
// read them twice.
func Verify(s *Store) (*Config, *Snapshot, error) {
	return nil, nil, errNotImplemented
}

// Replay derives State from the commits. It is deterministic: the same
// log always gives the same State, and the result equals the state.json a
// crash-free run would have written, because the controller applies each
// commit with the same applyCommit.
//
// Derivation rules (applyCommit):
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
// A commit whose layer is not in snap.Layers is ErrCorrupt.
func Replay(cfg *Config, snap *Snapshot, commits []Commit) (*State, error) {
	return nil, errNotImplemented
}

// LoadState returns the forum's current State: state.json when its Seq
// matches the last commit, otherwise a fresh Replay (which it writes back
// through WriteState so the next read is cheap). It is what every reader
// (status, results) and the controller use; nothing reads state.json
// directly.
func LoadState(s *Store, cfg *Config, snap *Snapshot) (*State, error) {
	return nil, errNotImplemented
}
