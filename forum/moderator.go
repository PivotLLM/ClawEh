// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"fmt"
	"time"
)

// Seam (d): moderation (rev 5 §6, rev 3 §6). The moderator is a
// participant invoked in a controller role after a round; its decision is
// JSON held to the layer's effective schema (Snapshot.ModeratorSchemas).

// moderatorDue reports whether the moderator is consulted after round:
// round >= m.AfterRound and (round - m.AfterRound) % m.EveryRounds == 0.
// The caller also requires round < max_rounds (rev 3 §6 "while below
// max_rounds").
func moderatorDue(m *Moderator, round int) bool {
	if m == nil || round < m.AfterRound {
		return false
	}
	return (round-m.AfterRound)%m.EveryRounds == 0
}

// runModerator performs the moderator check after round and returns the
// decision, or the reason the forum or layer must stop. It mirrors
// runTurn: the same attempt numbering, limits, request/reply records and
// reservation (TurnKind TurnModerator, ModeratorTurnID(round)), resend of
// an uncertain attempt and adoption of a saved accepted reply; the
// difference is the message (composeModeratorMessage) and the validation
// (parseDecision against c.decisionSchemas[layer.ID]). A decision already
// committed for this round (CommitModerated) is returned at once, so
// guidance is never duplicated. Exhausting the attempts returns
// EndModeratorFailed (§5 "moderator failure stops the run as failed").
func (c *Controller) runModerator(ctx context.Context, layer Layer, round int) (*Decision, EndReason, error) {
	if d := c.committedDecision(layer.ID, round); d != nil {
		return d, "", nil
	}
	m, ok := c.parts.Participants[layer.Moderator.Participant]
	if !ok {
		return nil, "", fmt.Errorf("%w: moderator %q is not in participants.json", ErrCorrupt, layer.Moderator.Participant)
	}
	schema := c.decisionSchemas[layer.ID]
	turn := ModeratorTurnID(round)
	prior := c.turnAttempts(layer.ID, turn)
	if last := lastAttempt(prior); last != nil && last.Reply != nil && len(last.Reply.Issues) == 0 {
		d, _ := parseDecision(last.Reply.Text, schema, layer.Moderator.AllowDirected, layer.Participants)
		return d, "", nil
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
			message, throughSeq, repair = repairMessage(decisionContract, last.Reply.Issues), last.Request.ThroughSeq, true
		default:
			message, throughSeq, err = c.composeModeratorMessage(layer, round, m, c.State().Seq)
			if err != nil {
				return nil, "", err
			}
		}
		now := time.Now()
		if reason := c.checkLimits(layer, 1, now); reason != "" {
			return nil, reason, nil
		}
		req := &AttemptRequest{
			Layer: layer.ID, Round: round, Turn: turn, Attempt: attempt, Kind: TurnModerator,
			Participant: m.ID, AgentID: m.AgentID, SentAt: now,
			WaitSeconds: int(c.wait(now) / time.Second), Repair: repair, ThroughSeq: throughSeq, Message: message,
		}
		reply, err := c.ask(ctx, req, func(text string) []string {
			_, issues := parseDecision(text, schema, layer.Moderator.AllowDirected, layer.Participants)
			return issues
		})
		if err != nil {
			return nil, c.hostFailure(m, err), nil
		}
		prior = c.turnAttempts(layer.ID, turn)
		if len(reply.Issues) > 0 {
			continue
		}
		d, _ := parseDecision(reply.Text, schema, layer.Moderator.AllowDirected, layer.Participants)
		return d, "", nil
	}
	return nil, EndModeratorFailed, nil
}

// committedDecision returns the decision committed after round, or nil.
func (c *Controller) committedDecision(layerID string, round int) *Decision {
	c.mu.Lock()
	defer c.mu.Unlock()
	ls := c.state.Layers[layerID]
	if ls == nil {
		return nil
	}
	for i := range ls.Decisions {
		if ls.Decisions[i].Round == round {
			return &ls.Decisions[i].Decision
		}
	}
	return nil
}

// decisionContract describes the moderator's required reply for a repair.
const decisionContract = "a JSON object matching the decision schema: decision (CONTINUE, GUIDE or STOP), reason, guidance (nonempty for GUIDE, null otherwise)"

// composeModeratorMessage builds the moderator's message after round (rev
// 3 §6): the brief on its first message; its instructions, the layer's
// instructions and its routed inputs (LayerInputs.Moderator) on its first
// turn in the layer; the layer's conversation since its last message
// (eligibleEvents, as published outputs or full outputs per
// Moderator.ConversationView); its own previous decisions in this layer
// since its last message; and the decision contract: the effective schema
// (Snapshot.ModeratorSchemas[layer.ID]) verbatim, with the meaning of
// CONTINUE, GUIDE and STOP and, when AllowDirected, of `directed`.
func (c *Controller) composeModeratorMessage(layer Layer, round int, m ParticipantRecord, cutoff int) (message string, throughSeq int, err error) {
	return "", cutoff, errNotImplemented
}

// parseDecision validates a moderator reply. The reply must be one JSON
// object (a fenced block is accepted), validated against the effective
// schema when non-nil; then `decision` must be a DecisionKind, GUIDE
// requires a nonempty `guidance` and CONTINUE/STOP a null one. When
// allowDirected is false a `directed` field is a violation of the closed
// schema; when true each entry must name one of the layer's participants
// (participants) with nonempty text. Issues make the attempt invalid and
// are sent back as a repair.
func parseDecision(text string, schema CompiledSchema, allowDirected bool, participants []string) (*Decision, []string) {
	return nil, []string{errNotImplemented.Error()}
}

// applyDecision commits the decision as CommitModerated and writes the
// public part to the transcript: the decision and reason, and the guidance
// for GUIDE. The assessment and the directed messages are never written to
// the transcript. It returns true when the layer must end
// (DecisionStop). Neither GUIDE nor CONTINUE overrides hard limits.
func (c *Controller) applyDecision(layer Layer, round int, d *Decision) (stop bool, err error) {
	if err := c.commit(&Commit{Kind: CommitModerated, Layer: layer.ID, Round: round, Turn: ModeratorTurnID(round), Decision: d}); err != nil {
		return false, err
	}
	if err := c.appendDecisionTranscript(layer, round, d); err != nil {
		return false, err
	}
	return d.Decision == DecisionStop, nil
}

// appendDecisionTranscript writes the public part of a decision to
// transcript.md.
func (c *Controller) appendDecisionTranscript(layer Layer, round int, d *Decision) error {
	return errNotImplemented
}
