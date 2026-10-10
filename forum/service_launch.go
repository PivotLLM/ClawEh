// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Launching a run (forum_validate, forum_launch): validating the forum's
// current configuration, writing the run, creating its temporary
// participants and starting it.

// Models lists the models agentID may give to fresh participants:
// Agents.Models(agentID) as is.
func (s *Service) Models(ctx context.Context, agentID string) ([]ModelInfo, error) {
	return s.host.Agents.Models(ctx, agentID)
}

// Validate runs decodeConfig, validateStatic and runPreflight on raw without
// creating anything (forum_validate). It returns nil, or a
// *ValidationError listing every finding.
func (s *Service) Validate(ctx context.Context, raw []byte, opts LaunchOptions) error {
	_, _, err := s.check(ctx, raw, opts)
	return err
}

// check is the shared validation path of Validate and Launch: decodeConfig,
// validateStatic, runPreflight with the service's host limits.
func (s *Service) check(ctx context.Context, raw []byte, opts LaunchOptions) (*Config, *resolvedConfig, error) {
	cfg, err := decodeConfig(raw)
	if cfg == nil {
		return nil, nil, err
	}
	// Every problem is reported at once: decodeConfig's and validateStatic's.
	decodeErr, _ := errors.AsType[*ValidationError](err)
	if staticErr := validateStatic(cfg); staticErr != nil || decodeErr != nil {
		var issues []Issue
		if decodeErr != nil {
			issues = append(issues, decodeErr.Issues...)
		}
		if ve, ok := errors.AsType[*ValidationError](staticErr); ok {
			issues = append(issues, ve.Issues...)
		} else if staticErr != nil {
			return nil, nil, staticErr
		}
		return nil, nil, &ValidationError{Issues: issues}
	}
	resolved, err := runPreflight(ctx, cfg, preflightEnv{
		Launcher:    opts.Scope.AgentID,
		Agents:      s.host.Agents,
		Ceilings:    s.Ceilings(),
		ResolveFile: opts.ResolveFile,
		ReadAllowed: opts.ReadAllowed,
	})
	if err != nil {
		return nil, nil, err
	}
	return cfg, resolved, nil
}

// Launch validates the forum's current configuration and starts a new run
// of it from the beginning, numbered after the latest (forum_launch),
// and returns the run's number. Steps, in order:
//
//  1. Serialise with the forum's other control operations and Lock the
//     forum (ErrLocked when another process has it). A running forum is
//     refused (errBusy). Runs whose launch did not finish are undone
//     first (undoUnstarted).
//  2. check (decodeConfig, validateStatic, runPreflight) the configuration.
//  3. CreateRun; write the run's forum.json (the configuration, indented).
//  4. Materialise every source (WriteSource): file sources from
//     resolvedConfig.SourceContents, inline ones from the configuration.
//  5. Create the temporary participants used by enabled layers
//     (createParticipants), recording each in the run's agents marker as
//     it is created.
//  6. WriteParticipants; set the run's notice marker; WriteSnapshot;
//     AppendCommit(CommitLaunched). The run is now durably accepted.
//  7. Open the run (openForum); end a paused previous run (supersede); start the run
//     goroutine.
//
// Launch is all or nothing for its caller: any failure, including openForum
// failing after step 6 (nothing has been dispatched yet), deletes the
// agents created so far and removes the run (revertRun), leaving the forum
// as it was, and the error is returned.
func (s *Service) Launch(ctx context.Context, id string, opts LaunchOptions) (int, error) {
	if s.isClosed() {
		return 0, errClosed
	}
	defer s.control(id)()
	r, err := s.live(ctx, opts.Scope, id)
	if err != nil {
		return 0, err
	}
	if r != nil {
		return 0, errBusy(s.ref(opts.Scope, id), r.ctrl.State().Status)
	}
	store, err := s.open(opts.Scope, id)
	if err != nil {
		return 0, err
	}
	if err = store.Lock(); err != nil {
		return 0, err
	}
	n, err := s.launchLocked(ctx, store, opts)
	if err != nil {
		store.Unlock()
		return 0, err
	}
	s.host.Logger.Infof("%s: launched by agent %s", logRun(store.Run(n)), opts.Scope.AgentID)
	return n, nil
}

// launchLocked is Launch once the forum is locked. On success the started
// run holds the lock; on failure the caller releases it.
func (s *Service) launchLocked(ctx context.Context, store *forumStore, opts LaunchOptions) (int, error) {
	started, err := s.undoUnstarted(ctx, store)
	if err != nil {
		return 0, err
	}
	var (
		prev        *forumStore
		prevDamaged bool
		prevPaused  bool
	)
	if len(started) > 0 {
		prev = store.Run(started[len(started)-1])
		_, _, st, loadErr := load(prev)
		switch {
		case errors.Is(loadErr, ErrCorrupt):
			prevDamaged = true
		case loadErr != nil:
			return 0, loadErr
		case busy(st.Status):
			return 0, errBusy(storeRef(store), st.Status)
		default:
			prevPaused = st.Status == StatusPaused
		}
	}
	current, _, err := store.ReadForumConfig()
	if err != nil {
		return 0, err
	}
	cfg, resolved, err := s.check(ctx, current, opts)
	if err != nil {
		return 0, err
	}
	raw, err := formatConfig(current)
	if err != nil {
		return 0, err
	}
	if opts.Origin.AgentID == "" {
		opts.Origin.AgentID = opts.Scope.AgentID
	}
	n, err := nextRun(store, started)
	if err != nil {
		return 0, err
	}
	run, err := store.CreateRun(n)
	if err != nil {
		return 0, err
	}
	if err = s.allocate(ctx, run, raw, cfg, resolved, opts); err != nil {
		return 0, errors.Join(err, s.revertRun(ctx, run))
	}
	ctrl, err := s.openCtrl(ctx, run, s.host)
	if err != nil {
		return 0, errors.Join(fmt.Errorf("forum %s run %d could not start: %w", store.ID(), n, err), s.revertRun(ctx, run))
	}
	if prevPaused || prevDamaged {
		// A closing service could not start the new run; the paused one must
		// then stay as it is.
		if s.isClosed() {
			return 0, errors.Join(errClosed, s.revertRun(ctx, run))
		}
		if err = s.supersede(ctx, prev, prevDamaged); err != nil {
			return 0, errors.Join(err, s.revertRun(ctx, run))
		}
	}
	// Recorded before the run starts, so even a run that ends at once
	// finds it.
	key := keyOf(run)
	s.setLaunchChat(key, Chat{Channel: opts.Origin.Channel, ChatID: opts.Origin.ChatID})
	if err = s.start(run, ctrl); err != nil { //nolint:contextcheck // the run outlives the launching call; its context is the service's
		s.takeLaunchChat(key)
		return 0, errors.Join(err, s.revertRun(ctx, run))
	}
	return n, nil
}

// allocate performs Launch steps 3 to 6 in the run's directory.
func (s *Service) allocate(ctx context.Context, store *forumStore, raw []byte, cfg *Config, resolved *resolvedConfig, opts LaunchOptions) error {
	if err := store.WriteConfig(raw); err != nil {
		return err
	}
	sources, err := materialiseSources(store, cfg, resolved)
	if err != nil {
		return err
	}
	parts, err := s.createParticipants(ctx, store, cfg, resolved, opts.Scope.AgentID)
	if err != nil {
		return err
	}
	if err = store.WriteParticipants(parts); err != nil {
		return err
	}
	if err = store.SetCleanup(cleanupNotice, []byte("pending\n")); err != nil {
		return err
	}
	seed, err := launchSeed(cfg)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	layers := make([]string, 0, len(cfg.Layers))
	for _, l := range cfg.EnabledLayers() {
		layers = append(layers, l.ID)
	}
	snap := &Snapshot{
		ForumID:          store.ID(),
		Run:              store.RunNumber(),
		Name:             cfg.Name,
		LaunchedAt:       now,
		BaseDirectory:    opts.Scope.BaseDirectory,
		Deadline:         now.Add(time.Duration(cfg.Limits.MaxDurationSeconds) * time.Second),
		Origin:           opts.Origin,
		ConfigDigest:     digest(raw),
		Seed:             seed,
		Limits:           cfg.Limits,
		Layers:           layers,
		ResultLayers:     cfg.EffectiveResultLayers(),
		Models:           resolved.Models,
		ModeratorSchemas: resolved.ModeratorSchemas,
		Sources:          sources,
	}
	if err = store.WriteSnapshot(snap); err != nil {
		return err
	}
	return store.AppendCommit(1, &Commit{Kind: CommitLaunched})
}

// materialiseSources copies every source into sources/: a file source
// from the bytes runPreflight read (the file is never reopened), an inline one from the configuration
// (a JSON string's text for text and markdown, the JSON value indented on
// its own for json).
func materialiseSources(store *forumStore, cfg *Config, resolved *resolvedConfig) (map[string]SourceRecord, error) {
	out := make(map[string]SourceRecord, len(cfg.Sources))
	for _, id := range sortedKeys(cfg.Sources) {
		src := cfg.Sources[id]
		var content []byte
		switch {
		case src.File != "":
			data, ok := resolved.SourceContents[id]
			if !ok {
				return nil, fmt.Errorf("source %q: file %q was not read by preflight", id, src.File)
			}
			content = data
		case src.Decode == FormatJSON:
			var b bytes.Buffer
			if err := json.Indent(&b, src.Inline, "", "  "); err != nil {
				return nil, fmt.Errorf("source %q: inline content: %w", id, err)
			}
			content = b.Bytes()
		default:
			var text string
			if err := json.Unmarshal(src.Inline, &text); err != nil {
				return nil, fmt.Errorf("source %q: inline content is not a JSON string: %w", id, err)
			}
			content = []byte(text)
		}
		rec, err := store.WriteSource(id, src.Decode, content)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", id, err)
		}
		out[id] = rec
	}
	return out, nil
}

// launchSeed is the configured seed, or a random nonnegative one.
func launchSeed(cfg *Config) (int64, error) {
	if cfg.Seed != nil {
		return *cfg.Seed, nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("generate seed: %w", err)
	}
	return int64(binary.LittleEndian.Uint64(b[:]) >> 1), nil
}

// createParticipants creates the temporary agents of every clone and
// fresh participant used by an enabled layer (as a participant or a
// moderator) and returns the records of every used participant, existing
// ones included; participants only named by disabled layers get no record.
// Each created agent is added to the cleanupAgents marker before the next
// is created, so whatever happens next its deletion is pending. On failure
// the caller discards the forum, which deletes the agents in the marker.
func (s *Service) createParticipants(ctx context.Context, store *forumStore, cfg *Config, resolved *resolvedConfig, launcher string) (*Participants, error) {
	used := (&preflight{cfg: cfg}).usedParticipants()
	out := &Participants{Participants: make(map[string]ParticipantRecord, len(used))}
	// Agents an earlier failed launch of this run could not delete stay
	// listed.
	created, err := agentsMarker(store)
	if err != nil {
		return nil, err
	}
	for _, id := range used {
		part := cfg.Participants[id]
		rec := ParticipantRecord{ID: id, Form: part.Form(), Name: part.Name}
		if rec.Name == "" {
			rec.Name = id
		}
		var err error
		switch rec.Form {
		case FormExisting:
			rec.AgentID = part.Agent
		case FormClone:
			rec.Model = resolved.Models[id]
			rec.AgentID, err = s.host.Agents.CreateClone(ctx, CloneSpec{Source: part.Clone, Model: rec.Model, Owner: launcher})
			if err != nil {
				return nil, fmt.Errorf("participant %q: could not clone agent %q: %w", id, part.Clone, err)
			}
		case FormFresh:
			rec.Model = resolved.Models[id]
			rec.Mode = part.Mode
			if rec.Mode == "" {
				rec.Mode = FreshModeMemory
			}
			rec.AgentID, err = s.host.Agents.CreateFresh(ctx, FreshSpec{
				Model: rec.Model, SystemPrompt: part.SystemPrompt, Mode: rec.Mode, Owner: launcher,
			})
			if err != nil {
				return nil, fmt.Errorf("participant %q: could not create a temporary agent on model %q: %w", id, rec.Model, err)
			}
		}
		if rec.Form != FormExisting {
			rec.Created = true
			created = append(created, rec.AgentID)
			if err := setAgentsMarker(store, created); err != nil {
				return nil, err
			}
		}
		out.Participants[id] = rec
	}
	return out, nil
}
