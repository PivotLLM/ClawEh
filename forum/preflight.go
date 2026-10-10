// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Preflight: the checks of a configuration that need the host (agents,
// models, schemas, source files, the install's ceilings). They run after
// validateStatic, at forum_validate and at forum_launch.

// preflightEnv is what runPreflight needs from the host.
type preflightEnv struct {
	// Launcher is the launching agent's ID.
	Launcher string
	Agents   Agents
	// Ceilings are the install's maximums on Config.Limits and on each
	// layer's max_calls; a zero field is no ceiling. A limit above its
	// ceiling is reported as an issue naming the ceiling (it is never capped
	// silently).
	Ceilings Ceilings
	// ResolveFile maps a source `file` reference to the absolute host path
	// the launching agent's file tools would read for it (workspace,
	// workspace folders and mounts), or fails with a message for the
	// agent. nil is a host wiring error once a file source is present.
	ResolveFile func(ref string) (absPath string, err error)
	// ReadAllowed reports whether the launching agent may read an absolute
	// path; a nil func allows nothing (every file source fails).
	ReadAllowed func(absPath string) error
}

// resolvedConfig is what runPreflight establishes and Launch records in the snapshot.
type resolvedConfig struct {
	// Models maps a participant ID to the model it runs on for this forum:
	// every fresh participant, plus clones with a `model` override. Resume
	// never substitutes another model.
	Models map[string]string
	// Schemas are the compiled named schemas.
	Schemas map[string]*compiledSchema
	// ModeratorSchemas maps a layer ID to its effective decision schema
	// (effectiveModeratorSchema), for every enabled layer with a moderator;
	// Launch stores them in Snapshot.ModeratorSchemas.
	ModeratorSchemas map[string]json.RawMessage
	// SourceContents maps a file source's ID to the content runPreflight read
	// (once, from the path it checked); Launch materialises exactly these
	// bytes and never reopens the file by path.
	SourceContents map[string][]byte
}

// runPreflight checks the configuration against the host without creating
// anything. It requires a configuration that passed
// validateStatic. It returns a *ValidationError listing every finding, or
// the resolvedConfig result. Only participants used by enabled layers (as
// participants or moderators) are checked. Checks:
//
//   - existing and clone participants: Agents.MayTarget(launcher, id) is
//     true and Agents.Exists(id) is true;
//   - a clone's `model` override is in Agents.Models(source);
//   - a fresh participant's model is in Agents.Models(launcher);
//   - neither the launcher nor a clone of it takes part in a forum with an
//     anonymous input (it could read the forum's files and so the authors);
//   - every named schema compiles, and every enabled layer's effective
//     moderator schema (effectiveModeratorSchema) compiles too;
//   - each file source resolves (ResolveFile) to a path ReadAllowed
//     accepts (both the named path and, through any symbolic link, its
//     target) and that exists as a regular file; it is read once, here,
//     into resolvedConfig.SourceContents; a json file source's content parses
//     as one JSON value;
//   - each limit, and each layer's max_calls, is within Ceilings.
func runPreflight(ctx context.Context, cfg *Config, env preflightEnv) (*resolvedConfig, error) {
	if env.Agents == nil || env.Launcher == "" {
		return nil, errors.New("preflight: launcher and Agents are required")
	}
	p := &preflight{cfg: cfg, env: env, models: map[string][]ModelInfo{}, res: &resolvedConfig{
		Models:           map[string]string{},
		Schemas:          map[string]*compiledSchema{},
		ModeratorSchemas: map[string]json.RawMessage{},
		SourceContents:   map[string][]byte{},
	}}
	if err := p.participants(ctx); err != nil {
		return nil, err
	}
	p.launcherClones()
	p.schemas()
	if err := p.sources(); err != nil {
		return nil, err
	}
	p.ceilings()
	if len(p.issues) > 0 {
		return nil, &ValidationError{Issues: p.issues}
	}
	return p.res, nil
}

// preflight accumulates the issues and results of runPreflight.
type preflight struct {
	cfg    *Config
	env    preflightEnv
	issues []Issue
	res    *resolvedConfig
	// models caches Agents.Models per agent ID.
	models map[string][]ModelInfo
}

func (p *preflight) addf(path, format string, args ...any) {
	p.issues = append(p.issues, Issue{Path: path, Message: fmt.Sprintf(format, args...)})
}

// usedParticipants returns the IDs of participants that take part in an
// enabled layer, as participants or moderators, sorted.
func (p *preflight) usedParticipants() []string {
	used := map[string]bool{}
	for _, l := range p.cfg.EnabledLayers() {
		for _, id := range l.Participants {
			used[id] = true
		}
		if l.Moderator != nil {
			used[l.Moderator.Participant] = true
		}
	}
	return slices.Sorted(maps.Keys(used))
}

func (p *preflight) participants(ctx context.Context) error {
	for _, id := range p.usedParticipants() {
		part, ok := p.cfg.Participants[id]
		if !ok {
			continue // reported by validateStatic
		}
		path := "participants." + id
		switch part.Form() {
		case FormExisting:
			if _, err := p.target(ctx, path+".agent", part.Agent); err != nil {
				return err
			}
		case FormClone:
			ok, err := p.target(ctx, path+".clone", part.Clone)
			if err != nil {
				return err
			}
			if ok && part.Model != "" {
				found, names, err := p.hasModel(ctx, part.Clone, part.Model)
				if err != nil {
					return err
				}
				if found {
					p.res.Models[id] = part.Model
				} else {
					p.addf(path+".model", "model %q is not one of agent %q's models (%s)", part.Model, part.Clone, names)
				}
			}
		case FormFresh:
			found, names, err := p.hasModel(ctx, p.env.Launcher, part.Model)
			if err != nil {
				return err
			}
			if found {
				p.res.Models[id] = part.Model
			} else {
				p.addf(path+".model", "model %q is not one of the launching agent's models (%s)", part.Model, names)
			}
		}
	}
	return nil
}

// target checks that the launcher may name agentID and that it exists.
// Existence is not checked (nor revealed) for an agent the launcher may
// not name.
func (p *preflight) target(ctx context.Context, path, agentID string) (bool, error) {
	allowed, err := p.env.Agents.MayTarget(ctx, p.env.Launcher, agentID)
	if err != nil {
		return false, fmt.Errorf("preflight: may %q target %q: %w", p.env.Launcher, agentID, err)
	}
	if !allowed {
		p.addf(path, "the launching agent may not use agent %q (not in its allowed agents)", agentID)
		return false, nil
	}
	exists, err := p.env.Agents.Exists(ctx, agentID)
	if err != nil {
		return false, fmt.Errorf("preflight: does agent %q exist: %w", agentID, err)
	}
	if !exists {
		p.addf(path, "agent %q does not exist", agentID)
		return false, nil
	}
	return true, nil
}

// hasModel reports whether model is in agentID's model list, and the list
// of names for the message.
func (p *preflight) hasModel(ctx context.Context, agentID, model string) (bool, string, error) {
	list, ok := p.models[agentID]
	if !ok {
		var err error
		if list, err = p.env.Agents.Models(ctx, agentID); err != nil {
			return false, "", fmt.Errorf("preflight: models of agent %q: %w", agentID, err)
		}
		p.models[agentID] = list
	}
	names := make([]string, 0, len(list))
	found := false
	for _, m := range list {
		names = append(names, m.Name)
		found = found || m.Name == model
	}
	if len(names) == 0 {
		return found, "none", nil
	}
	return found, strings.Join(names, ", "), nil
}

// schemas compiles every named schema and builds and compiles every
// enabled layer's effective moderator schema.
func (p *preflight) schemas() {
	for _, id := range sortedKeys(p.cfg.Schemas) {
		compiled, err := compileSchema(p.cfg.Schemas[id])
		if err != nil {
			p.addf("schemas."+id, "schema %q: %v", id, err)
			continue
		}
		p.res.Schemas[id] = compiled
	}
	for i, l := range p.cfg.Layers {
		if !l.IsEnabled() || l.Moderator == nil {
			continue
		}
		var assessment json.RawMessage
		if l.Moderator.Schema != "" {
			if _, ok := p.res.Schemas[l.Moderator.Schema]; !ok {
				continue // the named schema failed (reported above)
			}
			assessment = p.cfg.Schemas[l.Moderator.Schema]
		}
		path := layerPath(i) + ".moderator"
		eff, err := effectiveModeratorSchema(l, assessment)
		if err != nil {
			p.addf(path, "%v", err)
			continue
		}
		if _, err := compileSchema(eff); err != nil {
			p.addf(path, "layer %q: the moderator's decision schema does not compile: %v", l.ID, err)
			continue
		}
		p.res.ModeratorSchemas[l.ID] = eff
	}
}

// sources checks every file source, reads it once into
// resolvedConfig.SourceContents, and checks the content of json file sources.
func (p *preflight) sources() error {
	for _, id := range sortedKeys(p.cfg.Sources) {
		src := p.cfg.Sources[id]
		if src.File == "" {
			continue
		}
		if p.env.ResolveFile == nil {
			return fmt.Errorf("preflight: source %q: no file resolver", id)
		}
		path := "sources." + id + ".file"
		abs, err := p.env.ResolveFile(src.File)
		if err != nil {
			p.addf(path, "source %q: %q cannot be used: %v", id, src.File, err)
			continue
		}
		if !filepath.IsAbs(abs) {
			return fmt.Errorf("preflight: source %q: the resolver returned %q, which is not absolute", id, abs)
		}
		data, err := p.readSource(abs)
		if err != nil {
			p.addf(path, "source %q: %q %v", id, src.File, err)
			continue
		}
		if src.Decode == FormatJSON {
			if err := checkDuplicateKeys(data); err != nil {
				p.addf(path, "source %q: %q is not one valid JSON value: %v", id, src.File, issueText(err))
				continue
			}
		}
		p.res.SourceContents[id] = data
	}
	return nil
}

// readSource resolves abs (following symbolic links), checks that the
// launching agent may read both the named path and its target, and reads
// the target, which must be a regular file. The target is opened without
// following a link (a link swapped in after the check fails) and its type
// is checked on the open file, so what is returned is what was checked.
func (p *preflight) readSource(abs string) ([]byte, error) {
	target, err := p.readable(abs)
	if err != nil {
		return nil, err
	}
	f, err := openNoFollow(target) // the path passed ReadAllowed for the launching agent
	if err != nil {
		return nil, fmt.Errorf("cannot be read: %w", err)
	}
	data, err := readRegular(f)
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("cannot be read: %w", closeErr)
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// readRegular reads all of f, which must be a regular file.
func readRegular(f *os.File) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("cannot be read: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("is not a regular file")
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("cannot be read: %w", err)
	}
	return data, nil
}

// readable resolves abs (following symbolic links) and checks that the
// launching agent may read both the named path and its target and that the
// target is a regular file. It returns the resolved path.
func (p *preflight) readable(abs string) (string, error) {
	if p.env.ReadAllowed == nil {
		return "", errors.New("is not readable by the launching agent")
	}
	if err := p.env.ReadAllowed(abs); err != nil {
		return "", fmt.Errorf("is not readable by the launching agent: %w", err)
	}
	target, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("does not exist")
		}
		return "", fmt.Errorf("cannot be resolved: %w", err)
	}
	if target != abs {
		if terr := p.env.ReadAllowed(target); terr != nil {
			return "", fmt.Errorf("links to %q, which the launching agent may not read: %w", target, terr)
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("cannot be read: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("is not a regular file")
	}
	return target, nil
}

// issueText renders a *ValidationError's issues on one line.
func issueText(err error) string {
	ve, ok := errors.AsType[*ValidationError](err)
	if !ok {
		return err.Error()
	}
	parts := make([]string, 0, len(ve.Issues))
	for _, is := range ve.Issues {
		if is.Path != "" {
			parts = append(parts, is.Path+": "+is.Message)
		} else {
			parts = append(parts, is.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// ceilings rejects any limit, and any layer's max_calls, above the
// install's maximum.
func (p *preflight) ceilings() {
	l, c := p.cfg.Limits, p.env.Ceilings
	for _, f := range []struct {
		name           string
		value, ceiling int
	}{
		{"max_calls", l.MaxCalls, c.MaxCalls},
		{"max_duration_seconds", l.MaxDurationSeconds, c.MaxDurationSeconds},
		{"call_timeout_seconds", l.CallTimeoutSeconds, c.CallTimeoutSeconds},
		{"max_parallel_calls", l.MaxParallelCalls, c.MaxParallelCalls},
	} {
		if f.ceiling > 0 && f.value > f.ceiling {
			p.addf("limits."+f.name, "%d is more than this install allows (%d)", f.value, f.ceiling)
		}
	}
	if c.MaxCalls <= 0 {
		return
	}
	for i, layer := range p.cfg.Layers {
		if layer.MaxCalls > c.MaxCalls {
			p.addf(layerPath(i)+".max_calls", "layer %q: %d is more than this install allows (%d)", layer.ID, layer.MaxCalls, c.MaxCalls)
		}
	}
}
