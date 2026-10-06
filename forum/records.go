// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"encoding/json"
	"fmt"
	"time"
)

// Shared contract: the record types written to and read from a forum
// directory (spec §8, rev 3 §8) and the derived State. Every seam reads
// these; the store writes them. Changing a field here is a cross-seam
// change and is agreed between owners first (DESIGN.md).

// Status is a forum's lifecycle state (§9).
type Status string

// Run states. The last four are terminal. StatusNew is a forum's status
// before its first run: it has a configuration and no run, controller or
// state.
const (
	StatusNew        Status = "new"
	StatusQueued     Status = "queued"
	StatusRunning    Status = "running"
	StatusPausing    Status = "pausing"
	StatusPaused     Status = "paused"
	StatusCancelling Status = "cancelling"
	StatusCompleted  Status = "completed"
	StatusIncomplete Status = "incomplete"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
)

// Terminal reports whether no further controller action is possible.
func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusIncomplete, StatusFailed, StatusCancelled:
		return true
	case StatusNew, StatusQueued, StatusRunning, StatusPausing, StatusPaused, StatusCancelling:
		return false
	}
	return false
}

// EndReason says why a layer or the run ended (§3.2, §5, §8).
type EndReason string

// End reasons. EndRoundLimit, EndCallLimit and EndModeratorStop end a
// layer (§3.2, §5: a layer that runs every round ends with round_limit);
// the others end the run.
const (
	EndCompleted         EndReason = "completed"          // the run: every enabled layer ended without a run-ending reason
	EndRoundLimit        EndReason = "round_limit"        // the layer reached max_rounds
	EndCallLimit         EndReason = "call_limit"         // the layer exhausted its max_calls
	EndModeratorStop     EndReason = "moderator_stop"     // the moderator decided STOP
	EndForumCallLimit    EndReason = "forum_call_limit"   // limits.max_calls exhausted: run incomplete
	EndDeadline          EndReason = "deadline"           // limits.max_duration_seconds elapsed: run incomplete
	EndAttemptsExhausted EndReason = "attempts_exhausted" // a turn used max_attempts_per_turn without a valid output: run failed
	EndModeratorFailed   EndReason = "moderator_failed"   // the moderator produced no valid decision: run failed
	EndParticipantGone   EndReason = "participant_gone"   // a temporary participant disappeared (§8): run failed
	EndHostError         EndReason = "host_error"         // a transport or store error: run failed
	EndCorrupt           EndReason = "corrupt"            // the forum's records failed verification during the run: run failed
	EndCancelled         EndReason = "cancelled"          // forum_cancel
)

// Snapshot (snapshot.json) is everything launch resolved that a resume must
// not recompute (rev 3 §8: resolved models, base directory, source hashes,
// effective limits, seed, original deadline, effective moderator schemas).
// It is written before the first dispatch and never changed.
type Snapshot struct {
	ForumID string `json:"forum_id"`
	// Run is the run number (1, 2, 3, ... per forum).
	Run           int       `json:"run"`
	Name          string    `json:"name,omitempty"`
	LaunchedAt    time.Time `json:"launched_at"`
	BaseDirectory string    `json:"base_directory"`
	// Deadline is LaunchedAt + limits.max_duration_seconds; every wait is
	// bounded by it and restart does not extend it (§5).
	Deadline time.Time `json:"deadline"`
	Origin   Origin    `json:"origin"`
	// ConfigDigest is the hex SHA-256 of the run's forum.json; Verify
	// checks it, and the forum's current forum.json differs from the run's
	// when its digest differs.
	ConfigDigest string `json:"config_digest"`
	Seed         int64  `json:"seed"`
	// Limits are the configuration's limits as accepted (within host
	// ceilings).
	Limits Limits `json:"limits"`
	// Layers are the enabled layer IDs in execution order.
	Layers       []string `json:"layers"`
	ResultLayers []string `json:"result_layers"`
	// Models is Resolved.Models at launch: participant ID -> model name for
	// every fresh participant and every clone with a `model` override.
	Models map[string]string `json:"models"`
	// ModeratorSchemas maps a layer ID to its effective decision schema
	// (EffectiveModeratorSchema), for every enabled layer with a moderator.
	ModeratorSchemas map[string]json.RawMessage `json:"moderator_schemas,omitempty"`
	// Sources maps a source ID to its materialised copy under sources/.
	Sources map[string]SourceRecord `json:"sources,omitempty"`
}

// Label is how the forum is named to people: its configured name, or its
// ID when it has none.
func (s *Snapshot) Label() string {
	return forumLabel(s.Name, s.ForumID)
}

// Ref names a forum in a message to people or agents: "<name> (<id>)"
// when it has a name, else its ID. A name equal to the ID (a Label of an
// unnamed forum) counts as none.
func Ref(name, id string) string {
	if name == "" || name == id {
		return id
	}
	return name + " (" + id + ")"
}

// forumLabel is name, or id when name is empty.
func forumLabel(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

// SourceRecord is one materialised source (sources/<id><ext>).
type SourceRecord struct {
	Decode Format `json:"decode"`
	// File is the path relative to the run's directory.
	File string `json:"file"`
	// Digest is the hex SHA-256 of the file's content.
	Digest string `json:"digest"`
}

// ParticipantRecord is one entry of participants.json (§8): how a
// participant is realised for this forum. Written before the first dispatch.
type ParticipantRecord struct {
	ID   string          `json:"id"`
	Form ParticipantForm `json:"form"`
	// AgentID is the agent the participant runs as: the real agent's ID or
	// the temporary agent's UUID.
	AgentID string `json:"agent_id"`
	// Created is true when the forum created the agent and must delete it.
	Created bool   `json:"created"`
	Name    string `json:"name"`
	// Model is the resolved model (Snapshot.Models) when one applies.
	Model string `json:"model,omitempty"`
	// Mode is the fresh participant's mode; FreshModeSingleShot changes what
	// the controller sends it (§3.1).
	Mode FreshMode `json:"mode,omitempty"`
}

// Participants is participants.json: records keyed by participant ID.
type Participants struct {
	Participants map[string]ParticipantRecord `json:"participants"`
}

// InputKind is what an InputItem carries.
type InputKind string

// Input kinds.
const (
	InputSource InputKind = "source"
	InputOutput InputKind = "output"
)

// InputItem is one piece of routed input as delivered to one recipient:
// the projected content, attributed. Content is stored verbatim so the
// record shows exactly what the recipient was given.
type InputItem struct {
	// Route is the index into the consuming layer's inputs (or the
	// moderator's inputs when the item is in LayerInputs.Moderator).
	Route    int       `json:"route"`
	Kind     InputKind `json:"kind"`
	SourceID string    `json:"source_id,omitempty"`
	OutputID string    `json:"output_id,omitempty"`
	// LayerID, Round and Author identify a routed output's producer; Author
	// is the participant ID and AuthorName its transcript name.
	LayerID    string `json:"layer_id,omitempty"`
	Round      int    `json:"round,omitempty"`
	Author     string `json:"author,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	// Label is the output's anonymous label ("Response A"), set for every
	// output of a layer that some route reads anonymously; Anonymous marks
	// an item delivered by such a route, shown by its label alone.
	Label     string `json:"label,omitempty"`
	Anonymous bool   `json:"anonymous,omitempty"`
	Format    Format `json:"format"`
	Content   string `json:"content"`
}

// LayerInputs (layers/<id>/inputs.json) is a layer's resolved routing:
// what each participant, and the moderator, receives from sources and
// earlier layers. Random distribution is decided here and persisted before
// the first dispatch, so a resume never reshuffles (§4).
type LayerInputs struct {
	LayerID string `json:"layer_id"`
	// Participants maps a participant ID to its ordered, deduplicated items.
	Participants map[string][]InputItem `json:"participants"`
	// Moderator holds the moderator's items, when the layer has one.
	Moderator []InputItem `json:"moderator,omitempty"`
	// Missing lists the optional routes (by index) that supplied nothing.
	Missing []int `json:"missing,omitempty"`
}

// TurnKind distinguishes participant turns from moderator checks.
type TurnKind string

// Turn kinds.
const (
	TurnParticipant TurnKind = "participant"
	TurnModerator   TurnKind = "moderator"
)

// TurnID is the work ID of one participant turn within a layer
// ("r<round>-<participant>"); it names the directory
// layers/<layer>/calls/<turn-id>/. One output is ever committed per turn ID.
func TurnID(round int, participantID string) string {
	return fmt.Sprintf("r%03d-%s", round, participantID)
}

// ModeratorTurnID is the work ID of the moderator check after a round
// ("m<round>"). Its prefix differs from TurnID's, so no participant ID
// (not even "moderator") can produce a moderator work ID.
func ModeratorTurnID(round int) string {
	return fmt.Sprintf("m%03d", round)
}

// AttemptRequest (calls/<turn>/<attempt>/request.json) records exactly what
// was sent (§8). It is written, and the attempt committed (CommitAttempt),
// before the Ask; an attempt with no reply is one whose outcome is unknown
// (§8 restart).
type AttemptRequest struct {
	Layer       string    `json:"layer"`
	Round       int       `json:"round"`
	Turn        string    `json:"turn"`
	Attempt     int       `json:"attempt"`
	Kind        TurnKind  `json:"kind"`
	Participant string    `json:"participant"`
	AgentID     string    `json:"agent_id"`
	SentAt      time.Time `json:"sent_at"`
	// WaitSeconds is the Ask wait actually used (call_timeout_seconds
	// bounded by the run deadline).
	WaitSeconds int `json:"wait_seconds"`
	// Repair is true when the message carries the previous attempt's
	// validation errors instead of fresh content.
	Repair bool `json:"repair,omitempty"`
	// ThroughSeq is the last commit whose public events (peer outputs,
	// guidance, directed messages) the message includes; the participant's
	// next message starts after it.
	ThroughSeq int    `json:"through_seq"`
	Message    string `json:"message"`
}

// AttemptReply (calls/<turn>/<attempt>/reply.json) records what came back.
type AttemptReply struct {
	ReceivedAt time.Time `json:"received_at"`
	Outcome    Outcome   `json:"outcome"`
	Text       string    `json:"text"`
	// Issues are the validation errors when the reply was not accepted
	// (bad JSON, schema violation, missing decision); empty when accepted.
	Issues []string `json:"issues,omitempty"`
}

// AttemptRecord pairs a request with its reply; a nil Reply is uncertain.
type AttemptRecord struct {
	Request AttemptRequest
	Reply   *AttemptReply
}

// OutputRecord is the attributed artifact of one successful turn (§4). Its
// files live in the attempt directory that produced it (rev 3 §8).
type OutputRecord struct {
	OutputID      string `json:"output_id"`
	LayerID       string `json:"layer_id"`
	Round         int    `json:"round"`
	ParticipantID string `json:"participant_id"`
	Format        Format `json:"format"`
	// ContentFile is the full output, relative to the forum root:
	// layers/<layer>/calls/<turn>/<attempt>/output<ext>.
	ContentFile string `json:"content_file"`
	// PublishedFile is the published projection (published<ext> beside the
	// output); the same file as ContentFile unless `share` narrows a JSON
	// output.
	PublishedFile string `json:"published_file"`
	// Digest is the hex SHA-256 of ContentFile; Verify checks it.
	Digest string `json:"digest"`
	// PublishedDigest is the hex SHA-256 of PublishedFile (equal to Digest
	// when the two are the same file); Verify checks it.
	PublishedDigest string `json:"published_digest"`
	Turn            string `json:"turn"`
	Attempt         int    `json:"attempt"`
}

// CommitKind is the kind of one commit-log entry.
type CommitKind string

// Commit kinds, with the fields each sets besides Seq, At and Kind.
const (
	CommitLaunched        CommitKind = "launched"         // participants.json written; status queued -> running
	CommitLayerStarted    CommitKind = "layer_started"    // Layer; inputs.json written
	CommitAttempt         CommitKind = "attempt"          // Layer, Round, Turn, TurnKind, Participant, Attempt, ThroughSeq: a reserved attempt, written before its Ask
	CommitTurn            CommitKind = "turn"             // Layer, Round, Turn, Output: a committed (and, for per_turn, published) output
	CommitRoundPublished  CommitKind = "round_published"  // Layer, Round: after_round publication of the round's turns
	CommitModerated       CommitKind = "moderated"        // Layer, Round, Turn, Decision
	CommitLayerEnded      CommitKind = "layer_ended"      // Layer, Reason
	CommitPauseRequested  CommitKind = "pause_requested"  // status -> pausing
	CommitPaused          CommitKind = "paused"           // status -> paused (no dispatch in flight)
	CommitResumed         CommitKind = "resumed"          // status -> running
	CommitCancelRequested CommitKind = "cancel_requested" // status -> cancelling
	CommitEnded           CommitKind = "ended"            // Status (terminal), Reason, and Layer/Turn when a turn caused it
)

// Commit is one entry of the append-only commit log (commits/<seq>.json).
// Seq starts at 1 and has no gaps. The log is authoritative: State is
// rebuilt from it alone by Replay.
type Commit struct {
	Seq         int           `json:"seq"`
	At          time.Time     `json:"at"`
	Kind        CommitKind    `json:"kind"`
	Layer       string        `json:"layer,omitempty"`
	Round       int           `json:"round,omitempty"`
	Turn        string        `json:"turn,omitempty"`
	TurnKind    TurnKind      `json:"turn_kind,omitempty"`
	Participant string        `json:"participant,omitempty"`
	Attempt     int           `json:"attempt,omitempty"`
	ThroughSeq  int           `json:"through_seq,omitempty"`
	Output      *OutputRecord `json:"output,omitempty"`
	Decision    *Decision     `json:"decision,omitempty"`
	Status      Status        `json:"status,omitempty"`
	Reason      EndReason     `json:"reason,omitempty"`
}

// RoundDecision is a moderator decision with the round it followed.
type RoundDecision struct {
	Round    int      `json:"round"`
	Seq      int      `json:"seq"`
	Decision Decision `json:"decision"`
}

// LayerState is a layer's progress as derived from the commits.
type LayerState struct {
	Started   bool      `json:"started"`
	Ended     bool      `json:"ended"`
	EndReason EndReason `json:"end_reason,omitempty"`
	// Round is the round in progress (or the last one when Ended).
	Round int `json:"round"`
	// RoundsPublished counts after_round publications; for per_turn it is
	// the number of rounds whose every turn is committed.
	RoundsPublished int `json:"rounds_published"`
	// Calls counts CommitAttempt entries under this layer.
	Calls int `json:"calls"`
	// Outputs are the committed outputs in commit order.
	Outputs   []OutputRecord  `json:"outputs"`
	Decisions []RoundDecision `json:"decisions,omitempty"`
}

// State (state.json) is the derived view of a forum. It is a cache: the
// store rewrites it after every commit, and Replay rebuilds it from the
// commits when it is missing or stale.
type State struct {
	Status Status    `json:"status"`
	Reason EndReason `json:"reason,omitempty"`
	// Seq is the last commit applied.
	Seq int `json:"seq"`
	// Calls counts every CommitAttempt in the forum, replied or not
	// (restart resets no limits, §5).
	Calls     int                    `json:"calls"`
	Layers    map[string]*LayerState `json:"layers"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// LayerResult is one result layer's contribution to the manifest.
type LayerResult struct {
	LayerID   string         `json:"layer_id"`
	Ended     bool           `json:"ended"`
	EndReason EndReason      `json:"end_reason,omitempty"`
	Outputs   []OutputRecord `json:"outputs"`
}

// Result (result.json) is the manifest forum_results renders (ResultsView): the result
// layers' outputs, completeness and omissions. For a running forum the
// service builds a partial one from State with Complete false; the file is
// written only at a terminal state, before the launching agent is notified
// (§9).
type Result struct {
	ForumID string `json:"forum_id"`
	Run     int    `json:"run"`
	// Name labels the forum: its configured name, or its ID when it has none.
	Name       string    `json:"name"`
	Status     Status    `json:"status"`
	Reason     EndReason `json:"reason,omitempty"`
	LaunchedAt time.Time `json:"launched_at"`
	EndedAt    time.Time `json:"ended_at,omitempty"`
	// Complete is true only for StatusCompleted.
	Complete bool          `json:"complete"`
	Calls    int           `json:"calls"`
	Layers   []LayerResult `json:"layers"`
	// Omissions names what the result lacks: result layers that did not
	// end, turns without a committed output.
	Omissions []string `json:"omissions,omitempty"`
	// OtherLayers is set only when the result layers have no output: the
	// outputs of the other enabled layers, so a run that failed early
	// still shows its work (committed outputs once the run has ended,
	// published ones while it runs).
	OtherLayers []LayerResult `json:"other_layers,omitempty"`
	// Transcript is the path of transcript.md relative to the run's
	// directory.
	Transcript string `json:"transcript"`
}

// LayerProgress is one layer's line in a status summary.
type LayerProgress struct {
	LayerID   string    `json:"layer_id"`
	Enabled   bool      `json:"enabled"`
	Started   bool      `json:"started"`
	Ended     bool      `json:"ended"`
	EndReason EndReason `json:"end_reason,omitempty"`
	Round     int       `json:"round"`
	MaxRounds int       `json:"max_rounds"`
	Calls     int       `json:"calls"`
	Outputs   int       `json:"outputs"`
}

// ForumMeta (forum-meta.json) records whose forum it is: the agent that
// created it and when. It is written once, before forum.json, so a forum
// with a configuration always has one.
type ForumMeta struct {
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"created_at"`
}

// Summary is one forum's progress as forum_status reports it.
type Summary struct {
	ForumID string `json:"forum_id"`
	// Name labels the forum: its configured name, or its ID when it has none.
	Name string `json:"name"`
	// Run is the run the summary describes (0 for a new forum), Runs how
	// many the forum has, and ConfigChanged whether the forum's
	// configuration differs from the one its latest run used.
	Run           int             `json:"run,omitempty"`
	Runs          int             `json:"runs"`
	ConfigChanged bool            `json:"config_changed,omitempty"`
	Status        Status          `json:"status"`
	Reason        EndReason       `json:"reason,omitempty"`
	LaunchedAt    time.Time       `json:"launched_at,omitzero"`
	UpdatedAt     time.Time       `json:"updated_at"`
	Deadline      time.Time       `json:"deadline,omitzero"`
	Calls         int             `json:"calls"`
	MaxCalls      int             `json:"max_calls"`
	Layers        []LayerProgress `json:"layers"`
}
