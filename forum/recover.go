// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"fmt"
	"slices"
)

// Seam (d): opening a forum for execution (spec §8 restart). Open is the
// only constructor of Controller, used both right after Launch and on
// resume, so restart recovery is the ordinary start path.

// Open loads a forum from its store and returns a controller positioned
// at the first unfinished action. The store must be locked by the caller.
// Steps:
//
//  0. Remove the temporary entries a crashed writer left in the run
//     (SweepRun).
//  1. Verify the directory (Verify), rebuild State from the commit log
//     (ReplayState, never the state.json cache), read the log and every layer's attempts (Store.ListAttempts).
//  2. Compile the configured schemas (compileSchemas).
//  3. Read participants.json. For every participant with Created true,
//     check Agents.Exists; a missing one is recorded so that Run ends the
//     forum failed with EndParticipantGone instead of recreating it (§8).
//     For an existing participant a missing agent is EndHostError at its
//     first dispatch, not at Open.
//  4. Build the Router over the store's ReadFile.
//  5. Regenerate transcript.md from the log (renderTranscript) when it
//     differs, in place, so a crash between a publication commit and its
//     append (or during an earlier regeneration) leaves no gap, duplicate
//     or torn entry.
//
// A terminal forum opens too (Run returns its status at once); the service
// uses that to finish cleanup after a restart.
func Open(ctx context.Context, s *Store, host Host) (*Controller, error) {
	if err := s.SweepRun(); err != nil {
		return nil, fmt.Errorf("open forum: %w", err)
	}
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
	schemas, decisionSchemas, err := compileSchemas(cfg, snap)
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
	ref := storeRef(s)
	c := &Controller{
		ref:             ref,
		logName:         fmt.Sprintf("forum %s run %d", ref, snap.Run),
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
// layer ID). It returns empty maps when there are none.
func compileSchemas(cfg *Config, snap *Snapshot) (named, decision map[string]*compiledSchema, err error) {
	named, decision = map[string]*compiledSchema{}, map[string]*compiledSchema{}
	for name, raw := range cfg.Schemas {
		if named[name], err = compileSchema(raw); err != nil {
			return nil, nil, fmt.Errorf("compile schema %q: %w", name, err)
		}
	}
	for layerID, raw := range snap.ModeratorSchemas {
		if decision[layerID], err = compileSchema(raw); err != nil {
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
