// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Seam (d): one participant turn (spec §2.3, §5; rev 3 §8 attempt
// reservation and recovery): composing the message, the attempt loop with
// bounded repair, output validation and publication.

// runTurn performs one participant turn and returns its committed output,
// or the reason the forum or layer must stop. cutoff is the last commit
// seq the participant may see. Attempts are numbered from the attempts
// already reserved for this turn ID (CommitAttempt, uncertain ones
// included, §8) up to limits.max_attempts_per_turn:
//
//  1. Look at the latest reserved attempt (lastAttempt):
//     - it has an accepted reply (Issues empty) but no committed output:
//     the process died between reply and commit; adopt it (storeOutput)
//     without calling again (rev 3 §8 "validate and adopt complete saved
//     responses linked to reserved attempts before calling again");
//     - it has no reply: its outcome is unknown; resend its Message
//     unchanged as a new attempt;
//     - it has a rejected reply: the next attempt is a repair
//     (repairMessage with its Issues);
//     - there is none: compose the first message (composeTurnMessage).
//  2. checkLimits(layer, 1, now); then ask, which writes request.json,
//     commits CommitAttempt (the reservation), calls Messenger.Ask with
//     wait(now) and writes reply.json with the validation result.
//  3. An unsuccessful outcome is a rejected attempt with Issues
//     ["<outcome>"]. A successful one is validated with validateOutput;
//     Issues reject it and the next attempt is a repair.
//  4. An accepted reply is stored (publishedProjection, Store.WriteOutput
//     into the attempt directory) and committed as CommitTurn with a new
//     OutputRecord. The transcript is written by the caller when the
//     output is published.
//
// Exhausting the attempts returns EndAttemptsExhausted. A Messenger error
// returns EndHostError, except that an agent the host reports as gone for
// a Created participant returns EndParticipantGone. Rejected content is
// never published; every attempt stays on disk.
func (c *Controller) runTurn(ctx context.Context, layer Layer, round int, participantID string, cutoff int) (*OutputRecord, EndReason, error) {
	turn := TurnID(round, participantID)
	p, ok := c.parts.Participants[participantID]
	if !ok {
		return nil, "", fmt.Errorf("%w: participant %q is not in participants.json", ErrCorrupt, participantID)
	}
	prior := c.turnAttempts(layer.ID, turn)
	if last := lastAttempt(prior); last != nil && last.Reply != nil && len(last.Reply.Issues) == 0 {
		out, err := c.storeOutput(layer, &last.Request, last.Reply)
		return out, "", err
	}
	for attempt := len(prior) + 1; attempt <= c.cfg.Limits.MaxAttemptsPerTurn; attempt++ {
		var (
			message    string
			throughSeq int
			repair     bool
			err        error
		)
		switch last := lastAttempt(prior); {
		case last != nil && last.Reply == nil:
			message, throughSeq, repair = last.Request.Message, last.Request.ThroughSeq, last.Request.Repair
		case last != nil:
			message, throughSeq, repair = repairMessage(outputContract(layer.Output), last.Reply.Issues), last.Request.ThroughSeq, true
		default:
			message, throughSeq, err = c.composeTurnMessage(layer, round, p, cutoff)
			if err != nil {
				return nil, "", err
			}
		}
		now := time.Now()
		if reason := c.checkLimits(layer, 1, now); reason != "" {
			return nil, reason, nil
		}
		req := &AttemptRequest{
			Layer: layer.ID, Round: round, Turn: turn, Attempt: attempt, Kind: TurnParticipant,
			Participant: participantID, AgentID: p.AgentID, SentAt: now,
			WaitSeconds: int(c.wait(now) / time.Second), Repair: repair, ThroughSeq: throughSeq, Message: message,
		}
		reply, err := c.ask(ctx, req, func(text string) []string {
			_, issues := validateOutput(layer.Output, text, c.schemas[layer.Output.Schema])
			return issues
		})
		if err != nil {
			return nil, c.hostFailure(p, err), nil
		}
		prior = c.turnAttempts(layer.ID, turn)
		if len(reply.Issues) > 0 {
			continue
		}
		out, err := c.storeOutput(layer, req, reply)
		if err != nil {
			return nil, "", err
		}
		return out, "", nil
	}
	return nil, EndAttemptsExhausted, nil
}

// ask performs one attempt: it writes request.json, commits CommitAttempt
// (Layer, Round, Turn, TurnKind, Participant, Attempt, ThroughSeq), records
// cancelActive, calls Messenger.Ask with the request's wait, writes
// reply.json (Issues from validate when the outcome is OutcomeOK, the
// outcome name otherwise) and updates the attempts cache. validate is the
// per-kind check (validateOutput for a turn, parseDecision for the
// moderator) reduced to its issues. A ctx cancellation from RequestCancel
// is returned as the error; the request and its reservation stay on disk
// without a reply.
func (c *Controller) ask(ctx context.Context, req *AttemptRequest, validate func(text string) []string) (*AttemptReply, error) {
	return nil, errNotImplemented
}

// hostFailure maps a Messenger error to the run's end reason:
// EndParticipantGone when the participant was Created and the host says
// the agent does not exist, EndHostError otherwise.
func (c *Controller) hostFailure(p ParticipantRecord, err error) EndReason {
	return EndHostError
}

// storeOutput writes an accepted reply as the turn's output into its
// attempt directory, commits CommitTurn and returns the record.
func (c *Controller) storeOutput(layer Layer, req *AttemptRequest, reply *AttemptReply) (*OutputRecord, error) {
	content, _ := validateOutput(layer.Output, reply.Text, c.schemas[layer.Output.Schema])
	published, err := publishedProjection(layer.Output, content)
	if err != nil {
		return nil, err
	}
	out := &OutputRecord{
		OutputID: uuid.NewString(), LayerID: layer.ID, Round: req.Round, ParticipantID: req.Participant,
		Format: layer.Output.Format, Turn: req.Turn, Attempt: req.Attempt,
	}
	if err := c.store.WriteOutput(out, content, published); err != nil {
		return nil, err
	}
	if err := c.commit(&Commit{Kind: CommitTurn, Layer: layer.ID, Round: req.Round, Turn: req.Turn, Output: out}); err != nil {
		return nil, err
	}
	return out, nil
}

// turnAttempts returns the cached attempts of one turn ID, in order.
func (c *Controller) turnAttempts(layerID, turn string) []AttemptRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []AttemptRecord
	for _, a := range c.attempts[layerID] {
		if a.Request.Turn == turn {
			out = append(out, a)
		}
	}
	return out
}

// lastAttempt returns the newest attempt, or nil.
func lastAttempt(attempts []AttemptRecord) *AttemptRecord {
	if len(attempts) == 0 {
		return nil
	}
	return &attempts[len(attempts)-1]
}

// composeTurnMessage builds what the participant is sent for this turn
// (§2.3). The message is data sections, each attributed, never phrased as
// controller instructions:
//
//   - the brief, on the participant's first message in the forum
//     (ParticipantState.Briefed false);
//   - its private instructions, the layer's instructions and its routed
//     inputs (LayerInputs.Participants[id]), on its first turn in the
//     layer (Introduced[layer] false);
//   - the eligible peer events of this layer since its last message:
//     eligibleEvents(layer, ParticipantState.ThroughSeq, cutoff) rendered
//     with author names and the moderator's guidance (GUIDE decisions)
//     in commit order, never the participant's own outputs;
//   - its pending directed messages (pendingDirected), marked private;
//   - the output contract (outputContract).
//
// A FreshModeSingleShot participant gets everything every time: brief,
// instructions, layer instructions, routed inputs and the whole eligible
// transcript of the layer up to cutoff (§3.1).
//
// It returns the message and the ThroughSeq to record (cutoff).
func (c *Controller) composeTurnMessage(layer Layer, round int, p ParticipantRecord, cutoff int) (message string, throughSeq int, err error) {
	ps := c.participantState(p.ID)
	after := ps.ThroughSeq
	if p.Mode == FreshModeSingleShot {
		after = 0
	}
	_ = c.eligibleEvents(layer.ID, after, cutoff, p.ID)
	_ = c.pendingDirected(layer.ID, p.ID, after, cutoff)
	return "", cutoff, errNotImplemented
}

// participantState returns a copy of a participant's state (zero when
// none yet).
func (c *Controller) participantState(participantID string) ParticipantState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ps := c.state.Participants[participantID]; ps != nil {
		return *ps
	}
	return ParticipantState{}
}

// eligibleEvents returns, in sequence order, the public events of one
// layer with afterSeq < Seq <= throughSeq: CommitTurn (a published output
// in a per_turn layer, or one whose round has a CommitRoundPublished at or
// before throughSeq in an after_round layer) and CommitModerated with
// DecisionGuide (guidance only). Outputs authored by exclude are left out.
func (c *Controller) eligibleEvents(layerID string, afterSeq, throughSeq int, exclude string) []Commit {
	return nil
}

// pendingDirected returns the directed messages addressed to participantID
// in CommitModerated entries of this layer with afterSeq < Seq <=
// throughSeq, in order. They are delivered once, inside the next turn, and
// never written to the transcript (§6).
func (c *Controller) pendingDirected(layerID, participantID string, afterSeq, throughSeq int) []DirectedMessage {
	return nil
}

// outputContract describes the required reply for a repair or a first
// message: the format and, for JSON, the schema name.
func outputContract(out Output) string {
	if out.Schema != "" {
		return fmt.Sprintf("%s matching schema %q", out.Format, out.Schema)
	}
	return string(out.Format)
}

// validateOutput checks a reply against the layer's output contract and
// returns the content to store. text and markdown are kept as is with no
// structural check. json must hold exactly one JSON value (a fenced
// ```json block around it is accepted and the fence removed); the value is
// re-encoded canonically and, when schema is non-nil, validated; the
// SchemaViolationError messages are the issues. When out.Share is set,
// every share pointer must resolve (rev 3 §4 "missing share paths fail
// output validation"). A non-empty issues list means the attempt is
// rejected and nothing is stored.
func validateOutput(out Output, text string, schema CompiledSchema) (content []byte, issues []string) {
	return nil, []string{errNotImplemented.Error()}
}

// publishedProjection returns the published form of an accepted output:
// nil (meaning "same as the full output") unless the format is JSON and
// out.Share is set, in which case Project(full, *out.Share) (an empty
// share list publishes {}).
func publishedProjection(out Output, full []byte) ([]byte, error) {
	if out.Format != FormatJSON || out.Share == nil {
		return nil, nil
	}
	return Project(full, *out.Share)
}

// repairMessage is the follow-up sent after a rejected attempt: the
// validation issues, one per line, and a request to resend the complete
// reply in the required form (contract). It carries no new content.
func repairMessage(contract string, issues []string) string {
	return ""
}

// appendTurnTranscript writes a published output to transcript.md:
// a heading with the layer, round and the author's name, then the
// published projection. Called when the output is published, never
// before.
func (c *Controller) appendTurnTranscript(layer Layer, out *OutputRecord) error {
	return errNotImplemented
}
