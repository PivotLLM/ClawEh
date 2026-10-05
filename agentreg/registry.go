// ClawEh
// License: MIT

// Package agentreg is the agent registry: every agent the loop can run a turn
// for, with its origin. Config agents come from the configuration and are
// rebuilt from it on every reload. Temporary agents are created at run time
// (Create), get a UUID id, live until they are deleted or sit idle past their
// TTL, are kept across reloads, and are invisible to operators and routing:
// List, Default and ResolveRoute only ever see config agents.
//
// The registry does not know what an agent is. The host hands it a BuildFunc
// that turns a Spec into an instance (the agent loop builds the instance and
// registers its tools), a RetireFunc that releases what the host caches for an
// instance before it is closed, and an InsertedFunc told when a new temporary
// agent becomes visible. So the registry imports no agent-loop code and can be
// used by anything that needs to create or look up agents.
package agentreg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
)

// Origin says where an agent came from.
type Origin string

const (
	// OriginConfig is an agent defined in the configuration.
	OriginConfig Origin = "config"
	// OriginTemp is a temporary agent created at run time.
	OriginTemp Origin = "temp"
)

// DefaultTTL is how long a temporary agent may sit idle before the sweep
// deletes it, unless Create was given another TTL.
const DefaultTTL = 24 * time.Hour

// SweepInterval is how often RunSweeper looks for idle temporary agents.
const SweepInterval = 10 * time.Minute

// ErrNotFound is returned for an id the registry does not hold.
var ErrNotFound = errors.New("agent not found")

// ErrBusy is returned by Delete for an agent that is in a turn.
var ErrBusy = errors.New("agent is in a turn")

// ErrNotTemp is returned by Delete for a config agent.
var ErrNotTemp = errors.New("agent is not a temporary agent")

// ErrClosed is returned once the registry has been closed.
var ErrClosed = errors.New("agent registry is closed")

// errAbandoned is returned by Reload when its commit was vetoed.
var errAbandoned = errors.New("agentreg: reload abandoned")

// Instance is what the registry holds for an agent.
type Instance interface {
	Close() error
}

// Spec is everything the host needs to build an agent.
type Spec struct {
	// ID is the registry id: the normalized config id, or a UUID.
	ID string
	// Config is the agent's configuration. For a config agent it points into
	// the configuration; for a clone it is a copy of its source's current
	// configuration with ID set to the UUID, derived afresh at every build;
	// for a fresh temporary agent it is the configuration it was created with.
	// Never nil.
	Config *config.AgentConfig
	// Origin is where the agent came from.
	Origin Origin
	// SourceID is the config agent a clone was made from; empty otherwise.
	SourceID string
	// Workspace holds the prompt files, files/, skills, Maestro data, task
	// records and mounts. A clone shares its source's.
	Workspace string
	// StateDir holds the conversation archive (sessions/) and the cognitive
	// memory (cogmem/). It is the workspace for a config agent and
	// <CLAW_HOME>/internal/temp/<uuid> for a temporary one.
	StateDir string
	// Ephemeral marks a memory that is read but never written by the loop:
	// nothing is observed into it or consolidated. An ephemeral agent is not
	// saved across restarts.
	Ephemeral bool
	// Fresh marks a temporary agent that is not a clone. It gets no tools.
	Fresh bool
	// Owner is the agent that created a temporary agent on its own behalf
	// (see OwnedBy); empty otherwise. Saved across restarts with the agent.
	Owner string
}

// IsClone reports whether the spec is a clone of a config agent.
func (s Spec) IsClone() bool { return s.SourceID != "" }

// Label names the agent for logs: the config id, "alice (clone 1a2b3c4d)" for
// a clone of alice, or "temp 1a2b3c4d" for a fresh temporary agent.
func (s Spec) Label() string {
	switch {
	case s.Origin != OriginTemp:
		return s.ID
	case s.IsClone():
		return fmt.Sprintf("%s (clone %s)", s.SourceID, ShortID(s.ID))
	default:
		return "temp " + ShortID(s.ID)
	}
}

// ShortID is the first eight characters of a temporary agent's UUID.
func ShortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// Info describes a registry entry.
type Info struct {
	Spec     Spec
	Created  time.Time
	LastUsed time.Time
	TTL      time.Duration // zero for a config agent
	InTurn   bool
}

// BuildFunc builds the instance for spec against cfg, tools included.
type BuildFunc[T Instance] func(cfg *config.Config, spec Spec) (T, error)

// RetireFunc releases what the host caches for an instance (open sessions,
// tokens) before the registry closes it. An error refuses the close; the
// entry stays.
type RetireFunc[T Instance] func(spec Spec, inst T) error

// InsertedFunc is told when Create has made a new temporary agent visible
// (Get finds it), so the host can bring anything that is applied to every
// registered agent (MCP tools) up to date on it.
type InsertedFunc[T Instance] func(spec Spec, inst T)

// Hooks are the host functions the registry calls.
type Hooks[T Instance] struct {
	Build    BuildFunc[T]
	Retire   RetireFunc[T]
	Inserted InsertedFunc[T]
	// Now is the clock; nil means time.Now. Tests set it.
	Now func() time.Time
	// Owner marks the process that owns the data directory (the one holding
	// claw.lock: the gateway). Only the owner restores the saved temporary
	// agents, removes unsaved directories under internal/temp and writes
	// temp_agents.json. Any other process (`claw agent` alongside a running
	// service) does none of that, so it can never wipe the service's agents.
	Owner bool
}

// meta is the per-agent bookkeeping that survives a reload rebuilding the
// instance.
type meta struct {
	created  time.Time
	ttl      time.Duration
	lastUsed atomic.Int64 // unix nanoseconds
	busy     atomic.Int32 // turns in progress
}

func newMeta(now time.Time, ttl time.Duration) *meta {
	m := &meta{created: now, ttl: ttl}
	m.lastUsed.Store(now.UnixNano())
	return m
}

func (m *meta) touch(now time.Time) { m.lastUsed.Store(now.UnixNano()) }

func (m *meta) last() time.Time { return time.Unix(0, m.lastUsed.Load()) }

type entry[T Instance] struct {
	inst T
	spec Spec
	meta *meta
}

// Registry holds every agent, config and temporary.
type Registry[T Instance] struct {
	// reloadMu orders creations against reloads. A Reload holds it for
	// writing; Create holds it for reading from reading the configuration to
	// inserting the agent, so every temporary agent is built entirely before
	// a reload (which then rebuilds it) or entirely after (against the new
	// configuration). Creations still run in parallel with each other. Delete
	// and Sweep take only mu, and a reload reconciles with them at its commit.
	reloadMu sync.RWMutex

	mu        sync.RWMutex
	cfg       *config.Config
	build     BuildFunc[T]
	entries   map[string]*entry[T]
	order     []string // config agent ids, in configuration order
	defaultID string
	resolver  *routing.RouteResolver
	closed    bool
	// orphans are instances that could not be released while Close was
	// running; they are closed with the rest.
	orphans []*entry[T]

	retire    RetireFunc[T]
	inserted  InsertedFunc[T]
	now       func() time.Time
	statePath string // <CLAW_HOME>/internal/temp_agents.json; "" = not persisted

	// rootMu guards tempRoot. The owner's root is <CLAW_HOME>/internal/temp.
	// Any other process gets a private directory under the system temp
	// directory, made on first use and removed by Close, so it never puts
	// anything where the owner cleans up.
	rootMu      sync.Mutex
	tempRoot    string
	privateRoot bool

	persistMu sync.Mutex
}

// root returns the directory temporary agents are created under, making a
// non-owner's private one on first use.
func (r *Registry[T]) root() (string, error) {
	r.rootMu.Lock()
	defer r.rootMu.Unlock()
	if r.tempRoot != "" {
		return r.tempRoot, nil
	}
	dir, err := os.MkdirTemp("", "claw-temp-agents-")
	if err != nil {
		return "", fmt.Errorf("agentreg: create a private temp root: %w", err)
	}
	r.tempRoot = dir
	return dir, nil
}

// TempRoot is the directory temporary agents are created under; "" for a
// non-owner that has not created one yet.
func (r *Registry[T]) TempRoot() string {
	r.rootMu.Lock()
	defer r.rootMu.Unlock()
	return r.tempRoot
}

// New builds the config agents of cfg, then restores the temporary agents
// saved by a previous run.
func New[T Instance](cfg *config.Config, hooks Hooks[T]) (*Registry[T], error) {
	if hooks.Build == nil {
		return nil, errors.New("agentreg: Build hook is required")
	}
	now := hooks.Now
	if now == nil {
		now = time.Now
	}
	r := &Registry[T]{
		cfg:      cfg,
		build:    hooks.Build,
		retire:   hooks.Retire,
		inserted: hooks.Inserted,
		now:      now,
		resolver: routing.NewRouteResolver(cfg),
	}
	if hooks.Owner {
		r.tempRoot, r.statePath = tempPaths(cfg)
	} else {
		// No restore, no cleanup, no persistence, and a private root made on
		// first use.
		r.privateRoot = true
	}
	entries, order, defaultID, err := buildConfigEntries(cfg, hooks.Build, now())
	if err != nil {
		return nil, err
	}
	r.entries, r.order, r.defaultID = entries, order, defaultID
	r.restore()
	return r, nil
}

// buildConfigEntries builds every enabled config agent. The first enabled
// agent is the default unless one is marked default. On error the instances
// already built are closed.
func buildConfigEntries[T Instance](cfg *config.Config, build BuildFunc[T], now time.Time) (map[string]*entry[T], []string, string, error) {
	entries := make(map[string]*entry[T])
	var order []string
	defaultID := ""
	explicitDefault := false
	for i := range cfg.Agents.List {
		ac := &cfg.Agents.List[i]
		if !ac.IsEnabled() {
			logger.InfoCF("agent", "Skipping disabled agent", map[string]any{"agent_id": ac.ID})
			continue
		}
		spec := configSpec(cfg, ac)
		inst, err := build(cfg, spec)
		if err != nil {
			for _, e := range entries {
				closeQuietly(e.inst, e.spec)
			}
			return nil, nil, "", fmt.Errorf("agent %q: %w", spec.ID, err)
		}
		if _, dup := entries[spec.ID]; !dup {
			order = append(order, spec.ID)
		}
		entries[spec.ID] = &entry[T]{inst: inst, spec: spec, meta: newMeta(now, 0)}
		logger.InfoCF("agent", "Registered agent", map[string]any{
			"agent_id":  spec.ID,
			"name":      ac.Name,
			"workspace": spec.Workspace,
		})
		if defaultID == "" {
			defaultID = spec.ID
		}
		if ac.Default && !explicitDefault {
			defaultID = spec.ID
			explicitDefault = true
		}
	}
	return entries, order, defaultID, nil
}

// configSpec is the spec of a config agent: its state lives in its workspace.
func configSpec(cfg *config.Config, ac *config.AgentConfig) Spec {
	ws := ConfigWorkspace(ac, cfg.BaseDir())
	return Spec{
		ID:        routing.NormalizeAgentID(ac.ID),
		Config:    ac,
		Origin:    OriginConfig,
		Workspace: ws,
		StateDir:  ws,
	}
}

// Get returns the agent with the given id, config or temporary.
func (r *Registry[T]) Get(id string) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[routing.NormalizeAgentID(id)]
	if !ok {
		var zero T
		return zero, false
	}
	return e.inst, true
}

// GetConfigured returns the config agent with the given id; a temporary agent
// is not found. For the paths an operator or a client can name an agent on.
func (r *Registry[T]) GetConfigured(id string) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[routing.NormalizeAgentID(id)]
	if !ok || e.spec.Origin != OriginConfig {
		var zero T
		return zero, false
	}
	return e.inst, true
}

// Info describes the agent with the given id.
func (r *Registry[T]) Info(id string) (Info, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[routing.NormalizeAgentID(id)]
	if !ok {
		return Info{}, false
	}
	return e.info(), true
}

func (e *entry[T]) info() Info {
	return Info{
		Spec:     e.spec,
		Created:  e.meta.created,
		LastUsed: e.meta.last(),
		TTL:      e.meta.ttl,
		InTurn:   e.meta.busy.Load() > 0,
	}
}

// IsTemp reports whether id is a temporary agent.
func (r *Registry[T]) IsTemp(id string) bool {
	info, ok := r.Info(id)
	return ok && info.Spec.Origin == OriginTemp
}

// HomeID is the agent an agent answers to: a clone's source, or the id
// itself. Things keyed by a config agent (cron jobs, the conversation a
// sub-agent's late results go to) use it.
func (r *Registry[T]) HomeID(id string) string {
	if info, ok := r.Info(id); ok && info.Spec.IsClone() {
		return info.Spec.SourceID
	}
	return routing.NormalizeAgentID(id)
}

// List returns the config agent ids in configuration order.
func (r *Registry[T]) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.order)
}

// ListTemp returns the temporary agent ids, sorted.
func (r *Registry[T]) ListTemp() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tempIDsLocked()
}

func (r *Registry[T]) tempIDsLocked() []string {
	var ids []string
	for id, e := range r.entries {
		if e.spec.Origin == OriginTemp {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// All returns every agent id: config agents in configuration order, then the
// temporary agents. For the machinery that applies to every agent (tool
// registration, shutdown), never for what an operator sees.
func (r *Registry[T]) All() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append(slices.Clone(r.order), r.tempIDsLocked()...)
}

// DefaultID returns the default agent's id, or "" when there is none.
func (r *Registry[T]) DefaultID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultID
}

// Default returns the agent marked default in the configuration, or the first
// enabled one; the zero value when there is none. Never a temporary agent.
func (r *Registry[T]) Default() T {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.entries[r.defaultID]; ok {
		return e.inst
	}
	var zero T
	return zero
}

// ResolveRoute determines which config agent handles a message.
func (r *Registry[T]) ResolveRoute(input routing.RouteInput) routing.ResolvedRoute {
	r.mu.RLock()
	resolver := r.resolver
	r.mu.RUnlock()
	return resolver.ResolveRoute(input)
}

// BeginTurn records that a turn of agent id, on instance inst, started,
// touching its last-used time; the returned function ends it. A temporary
// agent in a turn is never deleted, disposed or replaced. current is false
// (and end a no-op) when inst is no longer the agent's registered instance —
// the agent was deleted, or a reload replaced it — so the caller can drop a
// turn that would otherwise run on a closed instance.
func (r *Registry[T]) BeginTurn(id string, inst T) (end func(), current bool) {
	r.mu.RLock()
	e, ok := r.entries[routing.NormalizeAgentID(id)]
	ok = ok && any(e.inst) == any(inst)
	if ok {
		e.meta.busy.Add(1)
		e.meta.touch(r.now())
	}
	r.mu.RUnlock()
	if !ok {
		return func() {}, false
	}
	return r.endTurn(e), true
}

// endTurn is the function that ends a turn begun on e.
func (r *Registry[T]) endTurn(e *entry[T]) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			e.meta.touch(r.now())
			e.meta.busy.Add(-1)
			if persisted(e.spec) {
				r.persist()
			}
		})
	}
}

// Close closes every instance. It runs once; later calls do nothing, and the
// registry refuses to create, delete or reload afterwards. Saved temporary
// agents stay on disk and are restored at the next start.
func (r *Registry[T]) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	entries := make([]*entry[T], 0, len(r.entries)+len(r.orphans))
	for _, e := range r.entries {
		entries = append(entries, e)
	}
	entries = append(entries, r.orphans...)
	r.orphans = nil
	r.mu.Unlock()
	for _, e := range entries {
		closeQuietly(e.inst, e.spec)
	}
	// A non-owner's temporary agents live only as long as the process.
	if root := r.TempRoot(); r.privateRoot && root != "" {
		removeDir(root)
	}
}

func closeQuietly[T Instance](inst T, spec Spec) {
	if err := inst.Close(); err != nil {
		logger.WarnCF("agent", "Failed to close agent",
			map[string]any{"agent_id": spec.ID, "agent": spec.Label(), "error": err.Error()})
	}
}

// Reload rebuilds the config agents from cfg with build, and rebuilds every
// temporary agent against cfg (a clone from its source's new configuration).
// A temporary agent cfg can no longer build (its model or clone source is
// gone) is deleted. A temporary agent in a turn is left exactly as it is: it
// keeps its instance and its old configuration until a later reload finds it
// idle and rebuilds it (or a sweep finds the configuration can no longer
// build it and deletes it); nothing else rebuilds it.
//
// Nothing changes until everything is built. Then, under the write lock,
// commit (when not nil) runs: returning false abandons the reload; otherwise
// the new set replaces the old one in that same step, so the caller can swap
// its own state with it. Creations and deletions that happened while the
// reload was building are kept. A ctx that ends before the commit, or a
// vetoed commit, leaves the registry as it was and closes what was built.
// The temporary instances replaced are released and closed; replaced config
// instances are left to the caller, as before.
func (r *Registry[T]) Reload(ctx context.Context, cfg *config.Config, build BuildFunc[T], commit func() bool) error {
	if build == nil {
		return errors.New("agentreg: Build hook is required")
	}
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	now := r.now()
	entries, order, defaultID, err := buildConfigEntries(cfg, build, now)
	if err != nil {
		return err
	}

	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		closeEntries(entries)
		return ErrClosed
	}
	var temps []*entry[T]
	for _, id := range r.tempIDsLocked() {
		temps = append(temps, r.entries[id])
	}
	r.mu.RUnlock()

	rebuilt := make(map[string]*entry[T]) // by id, sharing the old entry's meta
	doomed := make(map[string]string)     // id → reason
	for _, e := range temps {
		if e.meta.busy.Load() > 0 {
			continue // in a turn: left as it is
		}
		spec, reason := r.respec(cfg, e.spec, entries)
		if reason == "" {
			inst, buildErr := build(cfg, spec)
			if buildErr == nil {
				rebuilt[spec.ID] = &entry[T]{inst: inst, spec: spec, meta: e.meta}
				continue
			}
			reason = "rebuild failed: " + buildErr.Error()
		}
		doomed[e.spec.ID] = reason
	}

	abandon := func(err error) error {
		closeEntries(entries)
		closeEntries(rebuilt)
		return err
	}
	if err := ctx.Err(); err != nil {
		return abandon(fmt.Errorf("%w: %w", errAbandoned, err))
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return abandon(ErrClosed)
	}
	if commit != nil && !commit() {
		r.mu.Unlock()
		return abandon(errAbandoned)
	}
	var replaced []*entry[T]
	var drop []*entry[T]
	changed := false
	for id, cur := range r.entries {
		if cur.spec.Origin != OriginTemp {
			continue
		}
		nb, wasRebuilt := rebuilt[id]
		busy := cur.meta.busy.Load() > 0
		switch {
		case wasRebuilt && nb.meta == cur.meta && !busy:
			entries[id] = nb
			replaced = append(replaced, cur)
			delete(rebuilt, id)
		case doomed[id] != "" && !busy:
			drop = append(drop, cur)
			changed = true
		default:
			// In a turn, created while this reload was building, or a rebuild
			// for an entry that changed meanwhile: keep what is there.
			entries[id] = cur
		}
	}
	r.entries, r.order, r.defaultID = entries, order, defaultID
	r.resolver = routing.NewRouteResolver(cfg)
	r.cfg, r.build = cfg, build
	r.mu.Unlock()

	// Rebuilt instances nobody took (deleted, or busy, meanwhile).
	closeEntries(rebuilt)
	for _, old := range replaced {
		r.closeReplaced(old)
	}
	for _, e := range drop {
		r.dispose(e, doomed[e.spec.ID])
	}
	if changed || len(replaced) > 0 {
		r.persist()
	}
	return nil
}

// closeEntries closes the instances of entries nothing else references.
func closeEntries[T Instance](entries map[string]*entry[T]) {
	for _, e := range entries {
		closeQuietly(e.inst, e.spec)
	}
}

// closeReplaced releases and closes a temporary agent's instance that a
// reload replaced. If the host cannot release it (a session still held), it
// is left open rather than closed under its holder.
func (r *Registry[T]) closeReplaced(e *entry[T]) {
	if r.retire != nil {
		if err := r.retire(e.spec, e.inst); err != nil {
			logger.WarnCF("agent", "Replaced temporary agent instance left open until shutdown", map[string]any{
				"agent_id": e.spec.ID, "agent": e.spec.Label(), "error": err.Error(),
			})
			r.keepOrphan(e)
			return
		}
	}
	closeQuietly(e.inst, e.spec)
}

// keepOrphan holds an instance that could not be released so Close closes
// it; once Close has run, it is closed now.
func (r *Registry[T]) keepOrphan(e *entry[T]) {
	r.mu.Lock()
	if !r.closed {
		r.orphans = append(r.orphans, e)
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	closeQuietly(e.inst, e.spec)
}

// putBack returns an entry that could not be released to the map; once Close
// has run, it is closed instead so it does not leak.
func (r *Registry[T]) putBack(e *entry[T]) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		closeQuietly(e.inst, e.spec)
		return
	}
	if _, taken := r.entries[e.spec.ID]; taken {
		// Another instance holds the id now: this one has no place to go back
		// to, so it is closed rather than leaked.
		r.mu.Unlock()
		logger.WarnCF("agent", "Unreleased temporary agent instance closed: its id is taken",
			map[string]any{"agent_id": e.spec.ID, "agent": e.spec.Label()})
		closeQuietly(e.inst, e.spec)
		return
	}
	r.entries[e.spec.ID] = e
	r.mu.Unlock()
}

// respec derives a temporary agent's spec against cfg, whose config agents
// are entries: a clone takes its source's current configuration and
// workspace. A non-empty reason says why it can no longer be built.
func (r *Registry[T]) respec(cfg *config.Config, spec Spec, entries map[string]*entry[T]) (Spec, string) {
	if spec.IsClone() {
		src, ok := entries[spec.SourceID]
		if !ok || src.spec.Origin != OriginConfig {
			return spec, "clone source " + spec.SourceID + " is gone"
		}
		next, err := cloneSpec(src.spec, spec.ID, spec.StateDir, spec.Ephemeral)
		if err != nil {
			return spec, err.Error()
		}
		next.Owner = spec.Owner
		spec = next
	}
	if missing := missingModels(cfg, spec.Config); len(missing) > 0 {
		return spec, fmt.Sprintf("model(s) %v no longer configured", missing)
	}
	return spec, ""
}

// missingModels lists the models ac names that cfg has no enabled model for,
// matched by model_name or by the wire model id the way an agent's fallback
// chain resolves them. An agent with no models of its own uses the defaults
// and names nothing.
func missingModels(cfg *config.Config, ac *config.AgentConfig) []string {
	var missing []string
	for _, name := range ac.Models {
		found := false
		for i := range cfg.Models {
			m := &cfg.Models[i]
			if m.Enabled && (m.ModelName == name || m.Model == name) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, name)
		}
	}
	return missing
}

// dispose releases, closes and removes an entry that is no longer in the
// map. If the host cannot release it, the entry is put back and kept.
func (r *Registry[T]) dispose(e *entry[T], reason string) {
	if r.retire != nil {
		if err := r.retire(e.spec, e.inst); err != nil {
			r.putBack(e)
			logger.WarnCF("agent", "Temporary agent kept: it could not be released", map[string]any{
				"agent_id": e.spec.ID, "agent": e.spec.Label(), "reason": reason, "error": err.Error(),
			})
			return
		}
	}
	r.closeAndRemove(e, reason)
}

// closeAndRemove closes an entry's instance and removes its directory.
func (r *Registry[T]) closeAndRemove(e *entry[T], reason string) {
	closeQuietly(e.inst, e.spec)
	if err := os.RemoveAll(e.spec.StateDir); err != nil {
		logger.WarnCF("agent", "Failed to remove temporary agent directory", map[string]any{
			"agent_id": e.spec.ID, "agent": e.spec.Label(), "path": e.spec.StateDir, "error": err.Error(),
		})
	}
	logger.InfoCF("agent", "Deleted temporary agent", map[string]any{
		"agent_id": e.spec.ID, "agent": e.spec.Label(), "reason": reason,
		"age": r.now().Sub(e.meta.created).Round(time.Second).String(),
	})
}
