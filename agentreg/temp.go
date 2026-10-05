// ClawEh
// License: MIT

package agentreg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	cogmemstore "github.com/PivotLLM/cogmem/store"
	"github.com/google/uuid"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
)

// TempDirName is the directory under <CLAW_HOME>/internal that holds the
// temporary agents, one <uuid> directory each.
const TempDirName = "temp"

// StateFileName is the file under <CLAW_HOME>/internal that lists the
// temporary agents that survive a restart (those without an ephemeral
// memory).
const StateFileName = "temp_agents.json"

// Option configures Create.
type Option func(*createOptions)

type createOptions struct {
	ttl       time.Duration
	source    string
	ephemeral bool
	owner     string
	// The fresh-agent options; a clone refuses them.
	systemPrompt    string
	hasSystemPrompt bool
	noMemory        bool
	singleShot      bool
}

// freshOnly reports whether any option that applies only to a fresh agent
// was given.
func (o createOptions) freshOnly() bool { return o.hasSystemPrompt || o.noMemory || o.singleShot }

// mode is the fresh agent's mode the options select: single-shot wins over
// no memory, and memory is the default.
func (o createOptions) mode() Mode {
	switch {
	case o.singleShot:
		return ModeSingleShot
	case o.noMemory:
		return ModeNoMemory
	default:
		return ModeMemory
	}
}

// OwnedBy records the agent that creates the temporary agent (Spec.Owner),
// so that agent can be told apart from others after a restart.
func OwnedBy(agentID string) Option {
	return func(o *createOptions) { o.owner = routing.NormalizeAgentID(agentID) }
}

// Temp sets how long the agent may sit idle before the sweep deletes it
// (default DefaultTTL).
func Temp(ttl time.Duration) Option { return func(o *createOptions) { o.ttl = ttl } }

// CloneOf makes the agent a clone of the config agent srcID: the same
// workspace, tools, models and configuration (always its source's current
// ones), its own conversation, and a snapshot of the source's memory. The cfg
// passed to Create must then be the zero value.
func CloneOf(srcID string) Option { return func(o *createOptions) { o.source = srcID } }

// EphemeralMemory makes the agent's memory read-only to the loop: recalled
// from, but nothing is observed into it or consolidated. An ephemeral agent
// is not saved across restarts.
func EphemeralMemory() Option { return func(o *createOptions) { o.ephemeral = true } }

// WithSystemPrompt sets a fresh agent's whole system prompt (default
// DefaultSystemPrompt). Not for a clone.
func WithSystemPrompt(text string) Option {
	return func(o *createOptions) { o.systemPrompt, o.hasSystemPrompt = text, true }
}

// WithoutMemory gives a fresh agent no cognitive memory; it still keeps its
// conversation. Not for a clone.
func WithoutMemory() Option { return func(o *createOptions) { o.noMemory = true } }

// SingleShot makes a fresh agent keep nothing: no memory, and every turn
// starts on a blank context (the system prompt and the new message only).
// Not for a clone.
func SingleShot() Option { return func(o *createOptions) { o.singleShot = true } }

// Create adds a temporary agent and returns its UUID id. Without CloneOf the
// agent is fresh: cfg (its id ignored) with an empty workspace of its own
// under <CLAW_HOME>/internal/temp/<uuid>/workspace, no tools, no prompt files
// and no skills. Its system prompt is WithSystemPrompt's text or
// DefaultSystemPrompt, and its mode (ModeMemory unless WithoutMemory or
// SingleShot) decides its cognitive memory, overriding cfg.Cogmem. The memory
// snapshot and the build run outside the registry's lock, so creations run
// in parallel.
func (r *Registry[T]) Create(cfg config.AgentConfig, opts ...Option) (string, error) {
	id, _, err := r.create(cfg, false, opts)
	return id, err
}

// CreateInTurn is Create with a turn already begun on the new agent, before
// anything else can see it, so it cannot be deleted, disposed or replaced
// before its first turn; end ends that turn.
func (r *Registry[T]) CreateInTurn(cfg config.AgentConfig, opts ...Option) (id string, end func(), err error) {
	return r.create(cfg, true, opts)
}

func (r *Registry[T]) create(cfg config.AgentConfig, inTurn bool, opts []Option) (string, func(), error) {
	o := createOptions{ttl: DefaultTTL}
	for _, opt := range opts {
		opt(&o)
	}
	if o.ttl <= 0 {
		return "", nil, fmt.Errorf("agentreg: TTL must be positive, got %s", o.ttl)
	}
	if o.source != "" && !reflect.ValueOf(cfg).IsZero() {
		return "", nil, errors.New("agentreg: a clone takes its configuration from its source; pass a zero AgentConfig")
	}
	if o.source != "" && o.freshOnly() {
		return "", nil, errors.New("agentreg: a clone takes its prompt and memory from its source; WithSystemPrompt, WithoutMemory and SingleShot are for a fresh agent")
	}
	if o.hasSystemPrompt && strings.TrimSpace(o.systemPrompt) == "" {
		return "", nil, errors.New("agentreg: the system prompt is empty")
	}

	// Held for reading from reading the configuration to inserting the agent:
	// a reload either finished before (this agent is built against its
	// configuration) or starts after (and rebuilds this agent). Creations do
	// not wait for each other.
	r.reloadMu.RLock()
	defer r.reloadMu.RUnlock()

	r.mu.RLock()
	closed, current, build := r.closed, r.cfg, r.build
	var src *entry[T]
	if o.source != "" {
		if se, ok := r.entries[routing.NormalizeAgentID(o.source)]; ok && se.spec.Origin == OriginConfig {
			src = se
		}
	}
	r.mu.RUnlock()
	if closed {
		return "", nil, ErrClosed
	}

	root, err := r.root()
	if err != nil {
		return "", nil, err
	}
	id := uuid.NewString()
	stateDir := filepath.Join(root, id)
	spec, err := r.newTempSpec(cfg, o, src, id, stateDir)
	if err != nil {
		return "", nil, err
	}
	spec.Owner = o.owner
	if missing := missingModels(current, spec.Config); len(missing) > 0 {
		return "", nil, fmt.Errorf("agentreg: model(s) %v are not configured", missing)
	}
	if err = os.MkdirAll(spec.StateDir, 0o700); err != nil {
		removeDir(spec.StateDir) // whatever part of it was made
		return "", nil, fmt.Errorf("agentreg: create %s: %w", spec.StateDir, err)
	}
	if src != nil {
		snapshotMemory(src.spec, spec)
	}
	inst, err := build(current, spec)
	if err != nil {
		removeDir(spec.StateDir)
		return "", nil, fmt.Errorf("agentreg: build %s: %w", spec.Label(), err)
	}

	e := &entry[T]{inst: inst, spec: spec, meta: newMeta(r.now(), o.ttl)}
	if inTurn {
		e.meta.busy.Add(1)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		closeQuietly(inst, spec)
		removeDir(spec.StateDir)
		return "", nil, ErrClosed
	}
	r.entries[id] = e
	r.mu.Unlock()

	if r.inserted != nil {
		r.inserted(spec, inst)
	}
	if persisted(spec) {
		r.persist()
	}
	logger.InfoCF("agent", "Created temporary agent", map[string]any{
		"agent_id": id, "agent": spec.Label(), "source": spec.SourceID,
		"ephemeral_memory": spec.Ephemeral, "ttl": o.ttl.String(), "mode": string(spec.Mode),
	})
	end := func() {}
	if inTurn {
		end = r.endTurn(e)
	}
	return id, end, nil
}

// newTempSpec is the spec of a new temporary agent: a clone of src when the
// options name a source, otherwise a fresh agent with cfg.
func (r *Registry[T]) newTempSpec(cfg config.AgentConfig, o createOptions, src *entry[T], id, stateDir string) (Spec, error) {
	if o.source != "" {
		if src == nil {
			return Spec{}, fmt.Errorf("%w: clone source %q is not a configured agent", ErrNotFound, o.source)
		}
		return cloneSpec(src.spec, id, stateDir, o.ephemeral)
	}
	ac, err := copyAgentConfig(cfg)
	if err != nil {
		return Spec{}, err
	}
	prompt := DefaultSystemPrompt
	if o.hasSystemPrompt {
		prompt = o.systemPrompt
	}
	return freshSpec(ac, id, stateDir, o.mode(), prompt, o.ephemeral), nil
}

// freshSpec is the spec of a fresh temporary agent with configuration ac. The
// mode decides the cognitive memory, whatever ac says.
func freshSpec(ac config.AgentConfig, id, stateDir string, mode Mode, prompt string, ephemeral bool) Spec {
	ac.ID, ac.Default = id, false
	memory := mode == ModeMemory
	ac.Cogmem = &memory
	return Spec{
		ID: id, Config: &ac, Origin: OriginTemp, StateDir: stateDir,
		Workspace: freshWorkspace(stateDir), Ephemeral: ephemeral, Fresh: true,
		Mode: mode, SystemPrompt: prompt,
	}
}

// cloneSpec is the spec of a clone of src: a copy of src's current
// configuration with the clone's id, src's workspace, its own state dir.
func cloneSpec(src Spec, id, stateDir string, ephemeral bool) (Spec, error) {
	ac, err := copyAgentConfig(*src.Config)
	if err != nil {
		return Spec{}, err
	}
	ac.ID, ac.Default = id, false
	return Spec{
		ID: id, Config: &ac, Origin: OriginTemp, SourceID: src.ID,
		Workspace: src.Workspace, StateDir: stateDir, Ephemeral: ephemeral,
	}, nil
}

// freshWorkspace is a fresh temporary agent's own workspace.
func freshWorkspace(stateDir string) string { return filepath.Join(stateDir, "workspace") }

// persisted reports whether an agent is saved across restarts: a temporary
// agent whose memory is not ephemeral.
func persisted(spec Spec) bool { return spec.Origin == OriginTemp && !spec.Ephemeral }

func removeDir(path string) {
	if err := os.RemoveAll(path); err != nil {
		logger.WarnCF("agent", "Failed to remove temporary agent directory",
			map[string]any{"path": path, "error": err.Error()})
	}
}

// snapshotMemory copies the source's cognitive memory into the clone's state
// directory. Best-effort: a source without memory gives the clone an empty one.
func snapshotMemory(src, dst Spec) {
	from := cogmemstore.DBPath(cogmemhost.Dir(src.StateDir))
	if _, err := os.Stat(from); err != nil {
		return
	}
	dir := cogmemhost.Dir(dst.StateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logger.WarnCF("agent", "clone memory snapshot dir failed",
			map[string]any{"agent": dst.Label(), "error": err.Error()})
		return
	}
	if err := cogmemstore.Snapshot(context.Background(), from, cogmemstore.DBPath(dir)); err != nil {
		logger.WarnCF("agent", "clone memory snapshot failed",
			map[string]any{"agent": dst.Label(), "error": err.Error()})
	}
}

// copyAgentConfig deep-copies an agent configuration.
func copyAgentConfig(ac config.AgentConfig) (config.AgentConfig, error) {
	var out config.AgentConfig
	b, err := json.Marshal(ac)
	if err != nil {
		return out, fmt.Errorf("agentreg: copy agent config: %w", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("agentreg: copy agent config: %w", err)
	}
	return out, nil
}

// Delete removes a temporary agent: the host releases what it caches for it,
// its instance is closed and its directory removed. It refuses a config agent
// and an agent that is in a turn. Only the map update takes the registry's
// lock.
func (r *Registry[T]) Delete(id string) error {
	return r.deleteEntry(routing.NormalizeAgentID(id), "deleted")
}

func (r *Registry[T]) deleteEntry(id, reason string) error {
	r.mu.Lock()
	e, ok := r.entries[id]
	switch {
	case r.closed:
		r.mu.Unlock()
		return ErrClosed
	case !ok:
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	case e.spec.Origin != OriginTemp:
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotTemp, id)
	case e.meta.busy.Load() > 0:
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrBusy, e.spec.Label())
	}
	delete(r.entries, id)
	r.mu.Unlock()

	if r.retire != nil {
		if err := r.retire(e.spec, e.inst); err != nil {
			r.putBack(e)
			return fmt.Errorf("agentreg: delete %s: %w", e.spec.Label(), err)
		}
	}
	r.closeAndRemove(e, reason)
	if persisted(e.spec) {
		r.persist()
	}
	return nil
}

// Sweep deletes every temporary agent that is not in a turn and has been idle
// longer than its TTL, or that the current configuration can no longer build.
// It returns how many it deleted.
func (r *Registry[T]) Sweep(now time.Time) int {
	type victim struct{ id, reason string }
	var victims []victim
	r.mu.RLock()
	for _, id := range r.tempIDsLocked() {
		e := r.entries[id]
		if e.meta.busy.Load() > 0 {
			continue
		}
		if idle := now.Sub(e.meta.last()); idle > e.meta.ttl {
			victims = append(victims, victim{id, "idle " + idle.Round(time.Second).String()})
			continue
		}
		if _, reason := r.respec(r.cfg, e.spec, r.entries); reason != "" {
			victims = append(victims, victim{id, reason})
		}
	}
	r.mu.RUnlock()

	deleted := 0
	for _, v := range victims {
		if err := r.deleteEntry(v.id, v.reason); err != nil {
			logger.WarnCF("agent", "Temporary agent sweep skipped an agent",
				map[string]any{"agent_id": v.id, "error": err.Error()})
			continue
		}
		deleted++
	}
	return deleted
}

// RunSweeper sweeps every interval until stop is closed.
func (r *Registry[T]) RunSweeper(stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			r.Sweep(r.now())
		}
	}
}

// tempPaths returns where temporary agents live and the file that lists them.
// Without a data directory (a bare test configuration) they live under the
// agents base directory when that is absolute, otherwise the system temp
// directory, and are not persisted.
func tempPaths(cfg *config.Config) (root, statePath string) {
	if dataDir := cfg.DataDir(); dataDir != "" {
		internal := filepath.Join(dataDir, global.InternalDir)
		return filepath.Join(internal, TempDirName), filepath.Join(internal, StateFileName)
	}
	if base := cfg.BaseDir(); filepath.IsAbs(base) {
		return filepath.Join(base, ".temp-agents"), ""
	}
	return filepath.Join(os.TempDir(), "claw-temp-agents"), ""
}

// ConfigWorkspace is a config agent's workspace: its configured workspace, or
// <base_dir>/<id>, with the routing-default agent (empty or "main" id) at
// <base_dir>/default.
func ConfigWorkspace(ac *config.AgentConfig, baseDir string) string {
	if ac != nil && strings.TrimSpace(ac.Workspace) != "" {
		return expandHome(strings.TrimSpace(ac.Workspace))
	}
	id := "default"
	if ac != nil {
		if nid := routing.NormalizeAgentID(ac.ID); nid != "" && nid != routing.DefaultAgentID {
			id = nid
		}
	}
	return filepath.Join(baseDir, id)
}

func expandHome(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		logger.WarnCF("agent", "Failed to resolve home directory", map[string]any{"path": path, "error": err.Error()})
	}
	if len(path) > 1 && path[1] == '/' {
		return home + path[1:]
	}
	return home
}
