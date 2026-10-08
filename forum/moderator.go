// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Seam (d): moderation (rev 5 §6, rev 3 §6). The moderator is a
// participant invoked in a controller role after a round; its decision is
// JSON held to the layer's effective schema (Snapshot.ModeratorSchemas).

// moderatorDue reports whether the moderator is consulted after round:
// round >= m.AfterRound and (round - m.AfterRound) % m.EveryRounds == 0.
// The caller also requires round < max_rounds (rev 3 §6 "while below
// max_rounds").
func moderatorDue(m *Moderator, round int) bool {
	if m == nil || round < m.AfterRound || m.EveryRounds <= 0 {
		return false
	}
	return (round-m.AfterRound)%m.EveryRounds == 0
}

// runModerator performs the moderator check after round and returns the
// decision, or the reason the forum or layer must stop, or nil and "" when
// a pause or cancel interrupted it. It is the attempt loop of a turn
// (perform: attempt numbering, limits, request/reply records and
// reservation under ModeratorTurnID(round) with TurnKind TurnModerator,
// resend of an uncertain attempt, adoption of a saved accepted reply);
// only the message (composeModeratorMessage) and the validation
// (parseDecision against c.decisionSchemas[layer.ID]) differ. A decision
// already committed for this round is returned at once, so guidance is
// never duplicated. Exhausting the attempts returns EndModeratorFailed
// (§5 "moderator failure stops the run as failed").
func (c *Controller) runModerator(ctx context.Context, layer Layer, round int) (*Decision, EndReason, error) {
	if d := c.committedDecision(layer.ID, round); d != nil {
		return d, "", nil
	}
	m, ok := c.parts.Participants[layer.Moderator.Participant]
	if !ok {
		return nil, "", fmt.Errorf("%w: moderator %q is not in participants.json", ErrCorrupt, layer.Moderator.Participant)
	}
	schema := c.decisionSchemas[layer.ID]
	res, err := c.perform(ctx, work{
		layer: layer, round: round, turn: ModeratorTurnID(round), kind: TurnModerator, p: m,
		compose: func(cutoff int) (string, error) {
			return c.composeModeratorMessage(layer, round, m, cutoff)
		},
		contract: c.decisionContract(layer),
		validate: func(text string) []string {
			_, issues := parseDecision(text, schema, layer.Moderator.AllowDirected, layer.Participants)
			return issues
		},
		exhausted: EndModeratorFailed,
	}, c.State().Seq)
	if err != nil || res.reply == nil {
		return nil, res.reason, err
	}
	d, issues := parseDecision(res.reply.Text, schema, layer.Moderator.AllowDirected, layer.Participants)
	if len(issues) > 0 {
		return nil, "", fmt.Errorf("%w: accepted decision of %s/%s no longer validates: %s",
			ErrCorrupt, layer.ID, res.req.Turn, strings.Join(issues, "; "))
	}
	return d, "", nil
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
			d := ls.Decisions[i].Decision
			return &d
		}
	}
	return nil
}

// moderatorHeader is the first line of every moderator message.
func (c *Controller) moderatorHeader(layer Layer, round int) string {
	return fmt.Sprintf("Forum %q, layer %q, moderator check after round %d.", c.snap.Label(), layer.ID, round)
}

// headingConversation and headingDecisions are the moderator's sections.
const (
	headingConversation = "## Conversation since your last check"
	headingDecisions    = "## Your previous decisions in this layer"
	headingDecision     = "## Your decision"
)

// composeModeratorMessage builds the moderator's message after round (rev
// 3 §6): the brief on its first message in the forum; its instructions,
// its role, the layer's instructions and its routed inputs
// (LayerInputs.Moderator) on its first check in the layer; the layer's
// conversation since its last message (published or full outputs per
// Moderator.ConversationView); and the decision contract with the
// effective schema verbatim (sent with every request). A single_shot
// moderator gets everything every time, including its own previous
// decisions in the layer; one that keeps its conversation is never sent
// its own decisions again (§2.3).
func (c *Controller) composeModeratorMessage(layer Layer, round int, m ParticipantRecord, cutoff int) (string, error) {
	briefed, introduced, through := c.contact(m.ID, layer.ID)
	single := m.Mode == FreshModeSingleShot
	full := single || !introduced
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", c.moderatorHeader(layer, round))
	if single || !briefed {
		c.writeBrief(&b)
	}
	if full {
		c.writeIntro(&b, layer, m, []string{
			fmt.Sprintf("You moderate this layer; its participants are %s. Their task in this layer:", c.names(layer.Participants)),
		})
		inputs, err := c.store.ReadLayerInputs(layer.ID)
		if err != nil {
			return "", fmt.Errorf("compose %s/%s: %w", layer.ID, ModeratorTurnID(round), err)
		}
		// No anonymous route reaches a moderator: a layer that reads
		// anonymously has none (ValidateStatic).
		c.writeInputs(&b, inputs.Moderator, nil, "")
	}
	after := through
	if single {
		after = 0
	}
	events, err := c.layerEvents(layer, after, cutoff, layer.Moderator.ConversationView == ConversationViewFull)
	if err != nil {
		return "", err
	}
	var outputs []forumEvent
	for _, ev := range events {
		if ev.output != nil {
			outputs = append(outputs, ev)
		}
	}
	heading := headingConversation
	if full {
		heading = headingSoFar
	}
	if len(outputs) == 0 {
		fmt.Fprintf(&b, "\n%s\nNothing new has been published.\n", heading)
	} else {
		c.writeEvents(&b, heading, outputs, "")
	}
	if single {
		c.writePreviousDecisions(&b, layer.ID, round)
	}
	fmt.Fprintf(&b, "\n%s\n%s\n", headingDecision, c.decisionContract(layer))
	return b.String(), nil
}

// writePreviousDecisions lists the moderator's committed decisions in the
// layer before round (single_shot moderators only).
func (c *Controller) writePreviousDecisions(b *strings.Builder, layerID string, round int) {
	ls := c.layerState(layerID)
	var prev []RoundDecision
	for _, d := range ls.Decisions {
		if d.Round < round {
			prev = append(prev, d)
		}
	}
	if len(prev) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s\n", headingDecisions)
	for _, d := range prev {
		data, err := json.Marshal(d.Decision)
		if err != nil {
			continue
		}
		fmt.Fprintf(b, "\n### After round %d\n%s\n", d.Round, fence("json", string(data)))
	}
}

// names lists participants by name, comma-separated.
func (c *Controller) names(ids []string) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		if name := c.participantName(id); name != id {
			out[i] = fmt.Sprintf("%s (%s)", name, id)
		} else {
			out[i] = id
		}
	}
	return strings.Join(out, ", ")
}

// decisionContract describes the moderator's required reply: what each
// decision does, `directed` when the layer allows it, `assessment` when a
// schema is named, and the effective schema verbatim.
func (c *Controller) decisionContract(layer Layer) string {
	var b strings.Builder
	b.WriteString("Decide how this layer continues and reply with exactly one JSON object and nothing else:\n")
	b.WriteString("- \"decision\": \"CONTINUE\" (the next round runs; guidance null), \"GUIDE\" (the next round runs and your guidance is shown to every participant; guidance a nonempty string) or \"STOP\" (the layer ends now; guidance null).\n")
	b.WriteString("- \"reason\": why, in a sentence or two; it is published with the decision.\n")
	b.WriteString("- \"guidance\": as above.\n")
	if layer.Moderator.Schema != "" {
		b.WriteString("- \"assessment\": your private assessment, following the schema below; it is never published.\n")
	}
	if layer.Moderator.AllowDirected {
		fmt.Fprintf(&b, "- \"directed\" (optional): private notes, each {\"to\": <participant id>, \"text\": ...}, delivered only to that participant in its next turn and never published. Participant ids: %s.\n",
			strings.Join(layer.Participants, ", "))
	}
	if schema := c.snap.ModeratorSchemas[layer.ID]; len(schema) > 0 {
		b.WriteString("The reply must validate against this JSON Schema:\n")
		b.WriteString(fence("json", string(schema)))
	}
	return b.String()
}

// parseDecision validates a moderator reply. The reply must be one JSON
// object (a fenced block is accepted), validated against the effective
// schema when non-nil; independently of the schema, `decision` must be a
// DecisionKind, `reason` a string, `guidance` present, nonempty for GUIDE
// and null for CONTINUE and STOP, no member may be unknown, `directed` is
// a violation unless allowDirected, and each directed entry must name one
// of the layer's participants with nonempty text. Issues make the attempt
// invalid and are sent back as a repair.
func parseDecision(text string, schema *compiledSchema, allowDirected bool, participants []string) (*Decision, []string) {
	value, err := decodeJSONValue([]byte(unfence(text)))
	if err != nil {
		return nil, []string{"the reply is not exactly one valid JSON value: " + strings.TrimPrefix(err.Error(), "decode JSON: ")}
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, []string{"the reply must be a JSON object"}
	}
	content, err := encodeJSONValue(value)
	if err != nil {
		return nil, []string{err.Error()}
	}
	if issues := schemaIssues(schema, content); len(issues) > 0 {
		return nil, issues
	}
	var issues []string
	for _, key := range []string{"decision", "reason", "guidance"} {
		if _, ok := obj[key]; !ok {
			issues = append(issues, fmt.Sprintf("%q is required", key))
		}
	}
	for key := range obj {
		switch key {
		case "decision", "reason", "guidance", "assessment":
		case "directed":
			if !allowDirected {
				issues = append(issues, "\"directed\" is not allowed in this layer")
			}
		default:
			issues = append(issues, fmt.Sprintf("unknown member %q", key))
		}
	}
	if len(issues) > 0 {
		slices.Sort(issues)
		return nil, issues
	}
	var d Decision
	dec := json.NewDecoder(bytes.NewReader(content))
	if err := dec.Decode(&d); err != nil {
		if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
			return nil, []string{fmt.Sprintf("%q has the wrong type", typeErr.Field)}
		}
		return nil, []string{"the decision does not decode: " + err.Error()}
	}
	switch d.Decision {
	case DecisionGuide, DecisionContinue, DecisionStop:
		if issue := guidanceIssue(&d); issue != "" {
			issues = append(issues, issue)
		}
	default:
		issues = append(issues, fmt.Sprintf("\"decision\" must be CONTINUE, GUIDE or STOP, not %q", d.Decision))
	}
	for i, dm := range d.Directed {
		if !slices.Contains(participants, dm.To) {
			issues = append(issues, fmt.Sprintf("directed[%d]: %q is not a participant of this layer", i, dm.To))
		}
		if strings.TrimSpace(dm.Text) == "" {
			issues = append(issues, fmt.Sprintf("directed[%d]: text is empty", i))
		}
	}
	if len(issues) > 0 {
		return nil, issues
	}
	return &d, nil
}

// applyDecision commits the decision as CommitModerated and writes the
// public part to the transcript (publishTranscript): the decision and
// reason, and the guidance for GUIDE; the assessment and the directed
// messages never reach it. Nothing is committed once a cancel was
// requested. Neither GUIDE nor CONTINUE overrides hard limits; acting on
// STOP is the caller's.
func (c *Controller) applyDecision(layer Layer, round int, d *Decision) error {
	commit := &Commit{Kind: CommitModerated, Layer: layer.ID, Round: round, Turn: ModeratorTurnID(round), Decision: d}
	if err := c.commitWhen(notCancelling, commit, nil); err != nil {
		if errors.Is(err, errSkip) {
			return nil
		}
		return err
	}
	c.host.Logger.Infof("%s: layer %s after round %d: %s", c.logName, layer.ID, round, d.Decision)
	return c.publishTranscript(commit)
}
