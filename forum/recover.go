// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"fmt"
)

// Seam (d): opening a forum for execution (spec §8 restart). Open is the
// only constructor of Controller, used both right after Launch and on
// resume, so restart recovery is the ordinary start path.

// Open loads a forum from its store and returns a controller positioned
// at the first unfinished action. The store must be locked by the caller.
// Steps:
//
//  1. Verify the directory (Verify) and load State (LoadState) and every
//     layer's attempts (Store.ListAttempts).
//  2. Compile the configured schemas (compileSchemas); a configuration
//     naming a schema with a nil validator is ErrSchemasUnavailable.
//  3. Read participants.json. For every participant with Created true,
//     check Agents.Exists; a missing one is recorded (missing) so that Run
//     ends the forum failed with EndParticipantGone instead of recreating
//     it (§8). For an existing participant a missing agent is EndHostError
//     at its first dispatch, not at Open.
//  4. Build the Router over the store's ReadFile.
//
// A terminal forum opens too (Run returns its status at once); the service
// uses that to finish cleanup after a restart.
func Open(ctx context.Context, s *Store, host Host) (*Controller, error) {
	cfg, snap, err := Verify(s)
	if err != nil {
		return nil, fmt.Errorf("open forum: %w", err)
	}
	st, err := LoadState(s, cfg, snap)
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
	return &Controller{
		store:           s,
		cfg:             cfg,
		snap:            snap,
		parts:           parts,
		host:            host,
		schemas:         schemas,
		decisionSchemas: decisionSchemas,
		router:          NewRouter(cfg, snap, s.ReadFile),
		state:           st,
		attempts:        attempts,
		gone:            gone,
	}, nil
}

// compileSchemas compiles every entry of cfg.Schemas (named) and every
// effective moderator schema in snap.ModeratorSchemas (decision, keyed by
// layer ID). It returns empty maps when there are none, and
// ErrSchemasUnavailable when there are some and validator is nil.
func compileSchemas(cfg *Config, snap *Snapshot, validator SchemaValidator) (named, decision map[string]CompiledSchema, err error) {
	if len(cfg.Schemas) == 0 && len(snap.ModeratorSchemas) == 0 {
		return map[string]CompiledSchema{}, map[string]CompiledSchema{}, nil
	}
	if validator == nil {
		return nil, nil, ErrSchemasUnavailable
	}
	return nil, nil, errNotImplemented
}

// checkCreated returns the IDs of the participants the forum created
// (Created true) whose agent no longer exists, sorted. The error is for an
// Agents.Exists failure only. Open records the result in Controller.gone;
// a non-empty list makes Run end the forum failed with EndParticipantGone
// (§8), so the failure is reported through the ordinary path rather than
// as an error at Open.
func checkCreated(ctx context.Context, agents Agents, parts *Participants) ([]string, error) {
	return nil, errNotImplemented
}
