// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// Seam (d): opening a forum for execution (spec §8 restart). Open is the
// only constructor of Controller, used both right after Launch and on
// resume, so restart recovery is the ordinary start path.

// Open loads a forum from its store and returns a controller positioned
// at the first unfinished action. The store must be locked by the caller.
// Steps:
//
//  1. Verify the directory (Verify), rebuild State from the commit log
//     (ReplayState, never the state.json cache), read the log and every layer's attempts (Store.ListAttempts).
//  2. Compile the configured schemas (compileSchemas); a configuration
//     naming a schema with a nil validator is ErrSchemasUnavailable.
//  3. Read participants.json. For every participant with Created true,
//     check Agents.Exists; a missing one is recorded so that Run ends the
//     forum failed with EndParticipantGone instead of recreating it (§8).
//     For an existing participant a missing agent is EndHostError at its
//     first dispatch, not at Open.
//  4. Build the Router over the store's ReadFile.
//  5. Regenerate transcript.md from the log (renderTranscript) when it
//     differs, atomically, so a crash between a publication commit and
//     its append leaves no gap, duplicate or torn entry.
//
// A terminal forum opens too (Run returns its status at once); the service
// uses that to finish cleanup after a restart.
func Open(ctx context.Context, s *Store, host Host) (*Controller, error) {
	cfg, snap, err := Verify(s)
	if err != nil {
		return nil, fmt.Errorf("open forum: %w", err)
	}
	st, err := ReplayState(s, cfg, snap)
	if err != nil {
		return nil, fmt.Errorf("open forum: %w", err)
	}
	commits, err := s.ReadCommits()
	if err != nil {
		return nil, fmt.Errorf("open forum: %w", err)
	}
	attempts := make(map[string][]AttemptRecord, len(snap.Layers))
	for _, id := range snap.Layers {
		if attempts[id], err = s.ListAttempts(id); err != nil {
			return nil, fmt.Errorf("open forum: %w", err)
		}
	}
	schemas, decisionSchemas, err := compileSchemas(cfg, snap, host.Schemas)
	if err != nil {
		return nil, fmt.Errorf("open forum: %w", err)
	}
	parts, err := s.ReadParticipants()
	if err != nil {
		return nil, fmt.Errorf("open forum: %w", err)
	}
	gone, err := checkCreated(ctx, host.Agents, parts)
	if err != nil {
		return nil, fmt.Errorf("open forum: %w", err)
	}
	c := &Controller{
		store:           s,
		cfg:             cfg,
		snap:            snap,
		parts:           parts,
		host:            host,
		schemas:         schemas,
		decisionSchemas: decisionSchemas,
		router:          NewRouter(cfg, snap, s.ReadFile),
		state:           st,
		commits:         commits,
		attempts:        attempts,
		gone:            gone,
	}
	if err := c.regenerateTranscript(); err != nil {
		return nil, fmt.Errorf("open forum: %w", err)
	}
	return c, nil
}

// compileSchemas compiles every entry of cfg.Schemas (named) and every
// effective moderator schema in snap.ModeratorSchemas (decision, keyed by
// layer ID). It returns empty maps when there are none, and
// ErrSchemasUnavailable when there are some and validator is nil.
func compileSchemas(cfg *Config, snap *Snapshot, validator SchemaValidator) (named, decision map[string]CompiledSchema, err error) {
	named, decision = map[string]CompiledSchema{}, map[string]CompiledSchema{}
	if len(cfg.Schemas) == 0 && len(snap.ModeratorSchemas) == 0 {
		return named, decision, nil
	}
	if validator == nil {
		return nil, nil, ErrSchemasUnavailable
	}
	for name, raw := range cfg.Schemas {
		if named[name], err = validator.Compile(raw); err != nil {
			return nil, nil, fmt.Errorf("compile schema %q: %w", name, err)
		}
	}
	for layerID, raw := range snap.ModeratorSchemas {
		if decision[layerID], err = validator.Compile(raw); err != nil {
			return nil, nil, fmt.Errorf("compile moderator schema of layer %q: %w", layerID, err)
		}
	}
	return named, decision, nil
}

// checkCreated returns the IDs of the participants the forum created
// (Created true) whose agent no longer exists, sorted. The error is for an
// Agents.Exists failure only. Open records the result in Controller.gone;
// a non-empty list makes Run end the forum failed with EndParticipantGone
// (§8), so the failure is reported through the ordinary path rather than
// as an error at Open.
func checkCreated(ctx context.Context, agents Agents, parts *Participants) ([]string, error) {
	var gone []string
	for id, p := range parts.Participants {
		if !p.Created {
			continue
		}
		exists, err := agents.Exists(ctx, p.AgentID)
		if err != nil {
			return nil, fmt.Errorf("check participant %q (agent %s): %w", id, p.AgentID, err)
		}
		if !exists {
			gone = append(gone, id)
		}
	}
	slices.Sort(gone)
	return gone, nil
}

// transcript.md is derived: renderTranscript produces the whole file from
// the commit log and the published files, publishTranscript appends one
// publication live, and Open rewrites the file from the log when it
// differs. Only public material is rendered: published projections, and
// the decision, reason and guidance of moderator decisions.

// renderTranscript renders the transcript of every publication commit in
// the in-memory log. It returns the text and the seq of the last
// publication rendered.
func (c *Controller) renderTranscript() (string, int, error) {
	c.mu.Lock()
	commits := slices.Clone(c.commits)
	c.mu.Unlock()
	var (
		b    strings.Builder
		last int
	)
	for i := range commits {
		entry, err := c.transcriptEntry(&commits[i])
		if err != nil {
			return "", 0, err
		}
		if entry != "" {
			b.WriteString(entry)
			last = commits[i].Seq
		}
	}
	return b.String(), last, nil
}

// regenerateTranscript makes transcript.md equal renderTranscript,
// replacing it atomically only when it differs, and positions the live
// appends after the last rendered publication.
func (c *Controller) regenerateTranscript() error {
	want, last, err := c.renderTranscript()
	if err != nil {
		return err
	}
	have, err := c.store.ReadFile(fileTranscript)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		have = nil
	case err != nil:
		return err
	}
	if !bytes.Equal(have, []byte(want)) && (want != "" || have != nil) {
		c.host.Logger.Infof("forum %s: rebuilding %s from the commit log", c.snap.ForumID, fileTranscript)
		if err := c.durable("transcript", func() error {
			c.store.mu.Lock() // serialised with AppendTranscript
			defer c.store.mu.Unlock()
			return c.store.writeRel(fileTranscript, []byte(want), false)
		}); err != nil {
			return fmt.Errorf("rebuild transcript: %w", err)
		}
	}
	c.tmu.Lock()
	c.transcriptSeq = last
	c.tmu.Unlock()
	return nil
}

// publishTranscript appends the entry of one publication commit (a
// per_turn CommitTurn, a CommitRoundPublished, a CommitModerated) to
// transcript.md, once: a commit at or before the last one written is
// skipped.
func (c *Controller) publishTranscript(commit *Commit) error {
	c.tmu.Lock()
	defer c.tmu.Unlock()
	if commit.Seq <= c.transcriptSeq {
		return nil
	}
	entry, err := c.transcriptEntry(commit)
	if err != nil || entry == "" {
		return err
	}
	if err := c.durable("transcript", func() error { return c.store.AppendTranscript(entry) }); err != nil {
		return err
	}
	c.transcriptSeq = commit.Seq
	return nil
}

// transcriptEntry renders the public entry of one commit, or "" for a
// commit that publishes nothing.
func (c *Controller) transcriptEntry(commit *Commit) (string, error) {
	layer, ok := c.cfg.Layer(commit.Layer)
	if !ok {
		return "", nil
	}
	switch commit.Kind {
	case CommitTurn:
		if layer.Delivery != DeliveryPerTurn || commit.Output == nil {
			return "", nil
		}
		return c.outputEntry(layer, commit.Output)
	case CommitRoundPublished:
		ls := c.layerState(layer.ID)
		var round []OutputRecord
		for _, o := range ls.Outputs {
			if o.Round == commit.Round {
				round = append(round, o)
			}
		}
		var b strings.Builder
		for _, o := range orderOutputs(layer, round) {
			entry, err := c.outputEntry(layer, &o)
			if err != nil {
				return "", err
			}
			b.WriteString(entry)
		}
		return b.String(), nil
	case CommitModerated:
		if commit.Decision == nil || layer.Moderator == nil {
			return "", nil
		}
		return c.decisionEntry(layer, commit.Round, commit.Decision), nil
	case CommitLaunched, CommitLayerStarted, CommitAttempt, CommitLayerEnded, CommitPauseRequested,
		CommitPaused, CommitResumed, CommitCancelRequested, CommitEnded:
	}
	return "", nil
}

// outputEntry renders one published output: a heading with the layer,
// round and author's name, then the published projection (a JSON output
// in a json code fence).
func (c *Controller) outputEntry(layer Layer, out *OutputRecord) (string, error) {
	data, err := c.store.ReadFile(out.PublishedFile)
	if err != nil {
		return "", fmt.Errorf("transcript: read output %s: %w", out.OutputID, err)
	}
	body := strings.TrimRight(string(data), "\n")
	if out.Format == FormatJSON {
		body = fence("json", body)
	}
	return fmt.Sprintf("### %s · round %d · %s\n\n%s\n\n", layer.ID, out.Round, c.participantName(out.ParticipantID), body), nil
}

// decisionEntry renders the public part of a decision: the decision, the
// reason and, for GUIDE, the guidance. Never the assessment or directed
// messages.
func (c *Controller) decisionEntry(layer Layer, round int, d *Decision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s · after round %d · moderator %s: %s\n\n", layer.ID, round, c.participantName(layer.Moderator.Participant), d.Decision)
	if reason := strings.TrimSpace(d.Reason); reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n\n", reason)
	}
	if d.Decision == DecisionGuide && d.Guidance != nil {
		fmt.Fprintf(&b, "Guidance: %s\n\n", strings.TrimSpace(*d.Guidance))
	}
	return b.String()
}
