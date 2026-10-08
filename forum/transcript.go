// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// transcript.md is derived: renderTranscript produces the whole file from
// the commit log and the published files, publishTranscript appends one
// publication live, and openForum rewrites the file from the log when it
// differs. Only public material is rendered: published projections, and
// the decision, reason and guidance of moderator decisions.

// renderTranscript renders the transcript: a heading naming the forum and
// the run, then the entry of every publication commit in the in-memory
// log. It returns the text and the seq of the last publication rendered.
func (c *forumController) renderTranscript() (string, int, error) {
	c.mu.Lock()
	commits := slices.Clone(c.commits)
	c.mu.Unlock()
	var (
		b    strings.Builder
		last int
	)
	fmt.Fprintf(&b, "# %s · run %d\n\n", c.snap.Label(), c.snap.Run)
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
// rewriting it in place (forumStore.ReplaceTranscript) only when it differs,
// and positions the live appends after the last rendered publication.
func (c *forumController) regenerateTranscript() error {
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
		c.host.Logger.Infof("%s: rebuilding %s from the commit log", c.logName, fileTranscript)
		if err := c.durable("transcript", func() error { return c.store.ReplaceTranscript([]byte(want)) }); err != nil {
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
func (c *forumController) publishTranscript(commit *Commit) error {
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
func (c *forumController) transcriptEntry(commit *Commit) (string, error) {
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
// round and author's name (with the author's "Response X" label when some
// route reads the layer anonymously, so the transcript maps the letters
// the reviews use), then the published projection in a code fence of its
// format, so its own headings never mix with the transcript's.
func (c *forumController) outputEntry(layer Layer, out *OutputRecord) (string, error) {
	data, err := c.store.ReadFile(out.PublishedFile)
	if err != nil {
		return "", fmt.Errorf("transcript: read output %s: %w", out.OutputID, err)
	}
	author := c.participantName(out.ParticipantID)
	if c.router.readAnonymously(layer.ID) {
		author += " (" + responseLabel(slices.Index(layer.Participants, out.ParticipantID)) + ")"
	}
	body := fence(fenceInfo(out.Format), strings.TrimRight(string(data), "\n"))
	return fmt.Sprintf("### %s · round %d · %s\n\n%s\n\n", layer.ID, out.Round, author, body), nil
}

// decisionEntry renders the public part of a decision: the decision, the
// reason and, for GUIDE, the guidance. Never the assessment or directed
// messages.
func (c *forumController) decisionEntry(layer Layer, round int, d *Decision) string {
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
