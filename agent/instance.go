package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/cogmem"
	"github.com/PivotLLM/ctxengine"
	"github.com/PivotLLM/ctxengine/memory"
	"github.com/PivotLLM/ctxengine/session"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal/perms"
	agentws "github.com/PivotLLM/ClawEh/internal/workspace"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// AgentInstance represents a fully configured agent with its own workspace,
// session manager, context builder, and tool registry.
type AgentInstance struct {
	ID        string
	Name      string
	Model     string
	Fallbacks []string
	Workspace string
	// StateDir holds the agent's conversation archive (sessions/) and its
	// cognitive memory (cogmem/). It is the workspace for a config agent and
	// the agent's own directory under internal/temp for a temporary one.
	StateDir string
	// Spec is how the registry built the agent: its origin, the source of a
	// clone, whether its memory is ephemeral, whether it is a fresh temporary
	// agent (no tools).
	Spec           agentreg.Spec
	MaxIterations  int
	MaxTokens      int
	Temperature    float64
	ThinkingLevel  ThinkingLevel
	NoTools        bool
	ContextWindow  int
	CompressOpts   []ctxengine.Option
	Provider       providers.LLMProvider
	Sessions       session.SessionStore
	ContextBuilder *ContextBuilder
	Tools          *tools.ToolRegistry
	Subagents      *config.SubagentsConfig
	SkillsFilter   []string
	Candidates     []providers.FallbackCandidate

	// VisionClients is the ordered vision-describe side-model chain (from
	// AgentDefaults.VisionModel + VisionModelFallbacks). When the active model is
	// text-only and images are encountered, they are dispatched to these clients
	// (first success wins) for a one-shot text description instead of being
	// dropped. Empty = feature off. Wired by AgentLoop.agentBuilder.
	VisionClients []visionClient
	// EffectiveVisionModel is the first (primary) vision-describe model name, for
	// logging. Empty when no vision model is configured.
	EffectiveVisionModel string

	// Config is the agent's configuration, used for per-agent tool allowlists.
	Config *config.AgentConfig

	// DiscoveryActive is the effective progressive-discovery decision for this
	// agent (per-agent preference, overridden on by the auto_threshold). AgentLoop
	// sets it during tool registration; loop_mcp reads it to decide whether MCP
	// tools are registered hidden. When true, discovery-eligible tools (fusion and
	// maestro suites, all upstream MCP) are hidden behind search_tools /
	// get_tool_details; native tools and cogmem stay always-on.
	DiscoveryActive bool

	// AlwaysShownNamespaces pins discovery-eligible tool namespaces to always stay
	// in the model's tool list even when DiscoveryActive is true (matched by
	// config.MatchVisibility). Set from config alongside DiscoveryActive; read by
	// both the suite path and loop_mcp so a pinned maestro/fusion/MCP tool is
	// registered visible (core) instead of hidden. Empty pins nothing.
	AlwaysShownNamespaces []string

	// spawnMgr is the agent's sub-agent task manager, built with its tools; the
	// task supervisor scans the config agents' managers.
	spawnMgr *toolsagents.SubagentManager

	// HumanModel is set for a human agent: the model, on the human protocol,
	// that represents the person. Such an agent runs no model, has no tools
	// and no cognitive memory; its turns are answered in the person's chat
	// (runHumanTurn).
	HumanModel string
}

// Label names the agent in logs and audit rows: its id, or "alice (clone
// 1a2b3c4d)" for a temporary clone of alice.
func (a *AgentInstance) Label() string {
	if a.Spec.ID == "" {
		return a.ID
	}
	return a.Spec.Label()
}

// toolless reports whether the agent gets no tools at all: a fresh temporary
// agent, or a human agent (which runs no model to call them).
func (a *AgentInstance) toolless() bool { return a.Spec.Fresh || a.HumanModel != "" }

// IsTemp reports whether the agent is a temporary agent.
func (a *AgentInstance) IsTemp() bool { return a.Spec.Origin == agentreg.OriginTemp }

// toolIdentity is the agent id the agent's tools act as: a clone acts as its
// source (its Maestro projects, OAuth tokens, cron jobs and task ownership are
// the source's), every other agent as itself.
func (a *AgentInstance) toolIdentity() string {
	if a.Spec.IsClone() {
		return a.Spec.SourceID
	}
	return a.ID
}

// asyncResultTarget is where a late (async) result of a's tools is
// delivered: a's main conversation, or for a clone its source's — the clone
// may be deleted by the time the result arrives. Callers resolve it when the
// callback is built.
func asyncResultTarget(a *AgentInstance) (agentID, sessionKey string) {
	home := a.toolIdentity()
	return home, routing.BuildAgentMainSessionKey(home)
}

// NewAgentInstance creates a config agent's instance; its state lives in its
// workspace. A nil agentCfg is the routing-default agent.
func NewAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
) (*AgentInstance, error) {
	workspace := agentreg.ConfigWorkspace(agentCfg, cfg.BaseDir())
	spec := agentreg.Spec{Origin: agentreg.OriginConfig, Workspace: workspace, StateDir: workspace}
	if agentCfg != nil {
		spec.ID = routing.NormalizeAgentID(agentCfg.ID)
		spec.Config = agentCfg
	}
	return newAgentInstance(spec, defaults, cfg, provider)
}

// newAgentInstance creates the instance a registry spec describes.
func newAgentInstance(
	spec agentreg.Spec,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
) (*AgentInstance, error) {
	agentCfg := spec.Config
	workspace := spec.Workspace
	stateDir := spec.StateDir

	humanModel, human := cfg.HumanModelOf(agentCfg)
	if human {
		// A person's conversation is never given to a model, so a human agent
		// has no cognitive memory to observe into or consolidate.
		c := *agentCfg
		off := false
		c.Cogmem = &off
		agentCfg = &c
	}

	if spec.Fresh {
		// A fresh temporary agent's prompt is entirely its creator's: its
		// workspace is never seeded with prompt files or skills.
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			return nil, fmt.Errorf("create workspace %s: %w", workspace, err)
		}
	} else {
		agentws.Populate(workspace)
	}

	models := resolveAgentModels(agentCfg, defaults)
	model := ""
	var fallbacks []string
	if len(models) > 0 {
		model = models[0]
		fallbacks = models[1:]
	}

	restrict := defaults.RestrictToWorkspace
	_ = restrict // restrict is available to providers via cfg and defaults

	toolsRegistry := tools.NewToolRegistry()
	if agentCfg != nil {
		toolsRegistry.SetOwner(registryOwnerName(spec, agentCfg, cfg))
	}

	sessionsDir := filepath.Join(stateDir, "sessions")

	// Bring this agent's cognitive memory to the current layout and schema now,
	// rather than leaving it to be upgraded whenever it next happens to be
	// opened. Lazy migration spreads a schema change across hours of ordinary
	// use with no point an operator can call it done, and leaves a store
	// belonging to an agent nobody talks to that day on the old schema
	// indefinitely.
	migrateID := routing.DefaultAgentID
	if agentCfg != nil && agentCfg.ID != "" {
		migrateID = agentCfg.ID
	}
	if spec.Origin == agentreg.OriginTemp {
		migrateID = spec.Label()
	}
	if !spec.Fresh || agentCfg.CognitiveMemoryEnabled() {
		// A fresh agent without memory never gets a memory directory.
		cogmemhost.Migrate(migrateID, stateDir)
	}

	sessions, err := initSessionStore(sessionsDir)
	if err != nil {
		return nil, err
	}

	// The registry starts empty. Tools are registered exactly once — after
	// construction by AgentLoop.agentBuilder, and again on config reload —
	// so the full runtime deps (session closures, the sub-agent spawner, and the
	// per-agent message tool) are present. Registering here too would double-build
	// every tool and overwrite it, so we intentionally don't.

	// Progressive discovery is a single global switch; AgentLoop also sets it during
	// tool registration (and DiscoveryActive), so this just seeds the context rule.
	contextBuilder := NewContextBuilder(workspace).WithToolDiscovery(cfg.Tools.Discovery.Enabled)
	switch {
	case spec.Fresh:
		// The whole system prompt is the creator's (or the default): no
		// identity, prompt files, skills, memory guidance or runtime block.
		prompt := spec.SystemPrompt
		if strings.TrimSpace(prompt) == "" {
			prompt = agentreg.DefaultSystemPrompt // never the host prompt
		}
		contextBuilder = contextBuilder.WithFixedPrompt(prompt)
	case agentCfg.CognitiveMemoryEnabled():
		// Only an agent that has the subsystem is told how to use it.
		contextBuilder = contextBuilder.WithMemoryGuidance(cogmem.Guidance())
	}
	// For named agents, always apply the skills filter — even if empty.
	// nil filter = no restriction (all skills); empty filter = no skills.
	// Default/nil agentCfg means the default agent which gets all skills.
	if agentCfg != nil && agentCfg.Skills != nil && !spec.Fresh {
		contextBuilder = contextBuilder.WithSkillsFilter(agentCfg.Skills)
	}
	if agentCfg != nil {
		if mounts := agentCfg.EffectiveMounts(workspace); len(mounts) > 0 {
			contextBuilder = contextBuilder.WithMounts(mounts)
		}
		contextBuilder = contextBuilder.WithMaestro(agentCfg.MaestroEnabled())
	}

	agentID := routing.DefaultAgentID
	agentName := ""
	var subagents *config.SubagentsConfig
	var skillsFilter []string

	if agentCfg != nil {
		agentID = routing.NormalizeAgentID(agentCfg.ID)
		agentName = agentCfg.Name
		subagents = agentCfg.Subagents
		skillsFilter = agentCfg.Skills
	}

	if spec.Fresh {
		skillsFilter = []string{} // no skills: the prompt is the creator's
	}

	maxIter := defaults.MaxToolIterations
	if maxIter == 0 {
		maxIter = 20
	}

	maxTokens := defaults.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}

	temperature := global.DefaultTemperature
	if defaults.Temperature != nil {
		temperature = *defaults.Temperature
	}
	if agentCfg != nil && agentCfg.Temperature != nil {
		temperature = *agentCfg.Temperature
	}

	// Resolve the effective context window: prefer model-level override, fall back to
	// agent defaults, then a safe fallback of 128000.
	contextWindow := defaults.ContextWindow
	if contextWindow == 0 {
		contextWindow = 128000
	}

	var thinkingLevelStr string
	var noTools bool
	if mc, err := cfg.GetModelConfig(model); err == nil {
		thinkingLevelStr = mc.ThinkingLevel
		noTools = mc.NoTools
		if mc.ContextWindow > 0 {
			contextWindow = mc.ContextWindow
		}
		if mc.MaxTokens > 0 {
			maxTokens = mc.MaxTokens
		}
	}
	thinkingLevel := parseThinkingLevel(thinkingLevelStr)

	// Helper: resolve per-agent pointer or defaults int value.
	// For percent fields: 0 = not configured (use llmcontext default).
	// For count fields: 0 = explicitly disabled (valid to pass).
	resolveIntOpt := resolveAgentIntOpt

	var compressOpts []ctxengine.Option

	// Compaction policy: defaults block overlaid by the per-agent block, then
	// mapped to llmcontext options. Only fields the merged config actually sets
	// produce an option, so anything left unset keeps the llmcontext default.
	compressOpts = append(compressOpts,
		compressionOptions(agentCfg.EffectiveCompression(defaults.Compression))...)

	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.ArchiveMessageCount
		}
		return nil
	}(), defaults.ArchiveMessageCount); ok {
		compressOpts = append(compressOpts, ctxengine.WithArchiveMessageCount(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.ArchiveDays
		}
		return nil
	}(), defaults.ArchiveDays); ok {
		compressOpts = append(compressOpts, ctxengine.WithArchiveDays(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.SummaryMaxCount
		}
		return nil
	}(), defaults.SummaryMaxCount); ok {
		compressOpts = append(compressOpts, ctxengine.WithSummaryMaxCount(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.SummaryRetentionDays
		}
		return nil
	}(), defaults.SummaryRetentionDays); ok {
		compressOpts = append(compressOpts, ctxengine.WithSummaryRetentionDays(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.ArchiveContentMaxBytes
		}
		return nil
	}(), defaults.ArchiveContentMaxBytes); ok {
		compressOpts = append(compressOpts, ctxengine.WithArchiveContentMaxBytes(v))
	}

	// Resolve the per-turn eviction policy: built-in defaults, overlaid by the
	// defaults config block, overlaid by the per-agent block (field by field).
	evPolicy := ctxengine.DefaultEvictionPolicy()
	applyEvictionConfig(&evPolicy, defaults.ContextEviction)
	if agentCfg != nil {
		applyEvictionConfig(&evPolicy, agentCfg.ContextEviction)
	}
	compressOpts = append(compressOpts, ctxengine.WithEvictionPolicy(evPolicy))

	// Resolve fallback candidates
	modelCfg := providers.ModelConfig{Models: models}
	resolveFromModelList := func(raw string) (alias, model, provider string, ok bool) {
		raw = strings.TrimSpace(raw)
		if raw == "" || cfg == nil {
			return "", "", "", false
		}

		// Match by model_name alias first, then by raw model id.
		if mc, err := cfg.GetModelConfig(raw); err == nil && mc != nil && strings.TrimSpace(mc.Model) != "" {
			return mc.ModelName, mc.Model, mc.Provider, true
		}
		for i := range cfg.Models {
			if !cfg.Models[i].Enabled {
				continue
			}
			if strings.TrimSpace(cfg.Models[i].Model) == raw {
				return cfg.Models[i].ModelName, cfg.Models[i].Model, cfg.Models[i].Provider, true
			}
		}

		return "", "", "", false
	}

	candidates := providers.ResolveCandidatesWithLookup(modelCfg, "", resolveFromModelList)
	if len(candidates) == 0 {
		logger.ErrorCF("agent", "agent fallback chain is empty after resolving aliases",
			map[string]any{
				"agent_id":  agentID,
				"primary":   model,
				"fallbacks": fallbacks,
			})
	}

	// Normalize agentCfg to non-nil so Config is never nil after construction.
	// A nil config is equivalent to an empty allowlist (deny all tools).
	// IsToolAllowed() is already nil-safe, but callers should not need to guard on nil.
	if agentCfg == nil {
		agentCfg = &config.AgentConfig{Tools: []string{}}
	}

	if spec.ID == "" {
		spec.ID, spec.Config = agentID, agentCfg
	}

	return &AgentInstance{
		ID:             agentID,
		Name:           agentName,
		Model:          model,
		Fallbacks:      fallbacks,
		Workspace:      workspace,
		StateDir:       stateDir,
		Spec:           spec,
		MaxIterations:  maxIter,
		MaxTokens:      maxTokens,
		Temperature:    temperature,
		ThinkingLevel:  thinkingLevel,
		NoTools:        noTools,
		ContextWindow:  contextWindow,
		CompressOpts:   compressOpts,
		Provider:       provider,
		Sessions:       sessions,
		ContextBuilder: contextBuilder,
		Tools:          toolsRegistry,
		Subagents:      subagents,
		SkillsFilter:   skillsFilter,
		Candidates:     candidates,
		Config:         agentCfg,
		HumanModel:     humanModel,
	}, nil
}

// resolveAgentModels resolves the ordered model list for an agent: the agent's
// own Models when non-empty, otherwise the defaults' Models. Index 0 is the
// preferred model; the rest are fallbacks tried in order.
func resolveAgentModels(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) []string {
	if agentCfg != nil && len(agentCfg.Models) > 0 {
		return agentCfg.Models
	}
	return defaults.Models
}

// Close releases resources held by the agent's session store.
func (a *AgentInstance) Close() error {
	if a.Sessions != nil {
		return a.Sessions.Close()
	}
	return nil
}

// initSessionStore opens the per-session SQLite store under dir. The live
// window and the session state live in each session's archive DB alongside
// the archived messages, so there is one store, one seq space, and one place
// recovery reads.
func initSessionStore(dir string) (session.SessionStore, error) {
	if err := refuseUnmigratedSessions(dir); err != nil {
		return nil, err
	}
	// Created here, private, before the engine would create it with 0755.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory %s: %w", dir, err)
	}
	store, err := session.NewSQLiteStore(dir)
	if err != nil {
		return nil, fmt.Errorf("open session store %s: %w", dir, err)
	}
	return &privateSessionStore{SQLiteStore: store, dir: dir}, nil
}

// ensurePrivateArchive is ensureArchiveIn for the sessions directory of the
// state directory stateDir. The engine's own archive handle opens the same
// file, outside the session store, so the context manager calls it too.
func ensurePrivateArchive(stateDir, sessionKey string) {
	if stateDir == "" {
		return
	}
	ensureArchiveIn(filepath.Join(stateDir, "sessions"), sessionKey)
}

// ensureArchiveIn creates the session's archive database in dir as an empty
// 0600 file when it does not exist yet, so it and its -wal/-shm side files
// are private from the first write (SQLite would create them 0644). An
// existing file is left alone (startup tightens loose modes). Best-effort: a
// failure is logged, and the engine still opens the database.
func ensureArchiveIn(dir, sessionKey string) {
	if dir == "" || sessionKey == "" {
		return
	}
	path := memory.ArchivePath(dir, sessionKey)
	if _, err := os.Lstat(path); err == nil {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logger.WarnCF("agent", "Failed to create the session directory", map[string]any{"path": dir, "error": err.Error()})
		return
	}
	if err := perms.EnsurePrivateFile(path); err != nil {
		logger.WarnCF("agent", "Failed to create the session archive privately", map[string]any{"path": path, "error": err.Error()})
	}
}

// refuseUnmigratedSessions fails when dir still holds a JSONL-layout session
// (a `<key>.meta.json` that has not been renamed to `.migrated`). Starting on
// such a directory would be silently destructive: the first message (a cron
// fire is enough) mints a fresh window at seq 1 in the archive DB, after which
// `claw sessions migrate` sees a populated session, skips it, and the old
// history is stranded. Failing loudly makes the ordering mistake visible.
func refuseUnmigratedSessions(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // a fresh workspace has nothing to migrate
		}
		return fmt.Errorf("read sessions directory %s: %w", dir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".meta.json") {
			return fmt.Errorf("sessions directory %s still holds JSONL-layout sessions; "+
				"stop the service and run \"claw sessions migrate\" first", dir)
		}
	}
	return nil
}

// resolveAgentIntOpt resolves a per-agent integer config knob against the
// agents.defaults value. A non-nil per-agent pointer always wins (even when it
// points to 0, an explicit "disabled"); otherwise a non-zero default applies;
// otherwise the knob is unset (ok=false) and the llmcontext package default is
// used. Shared by every int-valued compress/retention option wired in
// initialization.
func resolveAgentIntOpt(agentPtr *int, defaultsVal int) (int, bool) {
	if agentPtr != nil {
		return *agentPtr, true
	}
	if defaultsVal != 0 {
		return defaultsVal, true
	}
	return 0, false
}

// applyEvictionConfig overlays a ContextEvictionConfig block onto an
// EvictionPolicy, leaving fields the block does not set untouched. Passing nil
// is a no-op, so callers can chain defaults then per-agent without nil guards.
func applyEvictionConfig(p *ctxengine.EvictionPolicy, c *config.ContextEvictionConfig) {
	if c == nil {
		return
	}
	if c.Enabled != nil {
		p.Enabled = *c.Enabled
	}
	if c.ProtectTurns != nil {
		p.ProtectTurns = *c.ProtectTurns
	}
	if c.EvictTurns != nil {
		p.EvictTurns = *c.EvictTurns
	}
	if c.BudgetBytes != nil {
		p.BudgetBytes = *c.BudgetBytes
	}
	if c.NotifyUser != nil {
		p.NotifyUser = *c.NotifyUser
	}
	if c.ArgBytes != nil {
		p.ArgBytes = *c.ArgBytes
	}
}

// compressionOptions maps a merged CompressionConfig onto llmcontext options.
// A nil field yields no option, so llmcontext's own default applies; an
// explicitly-set 0 is passed through, which is how a trigger gets disabled.
func compressionOptions(c *config.CompressionConfig) []ctxengine.Option {
	if c == nil {
		return nil
	}
	var opts []ctxengine.Option
	if c.TargetPercent != nil {
		opts = append(opts, ctxengine.WithTargetPercent(*c.TargetPercent))
	}
	if t := c.Trigger; t != nil {
		if t.MinPercent != nil {
			opts = append(opts, ctxengine.WithMinPercent(*t.MinPercent))
		}
		if t.NormalPercent != nil {
			opts = append(opts, ctxengine.WithNormalPercent(*t.NormalPercent))
		}
		if t.SafetyPercent != nil {
			opts = append(opts, ctxengine.WithSafetyPercent(*t.SafetyPercent))
		}
		if t.MessageCount != nil {
			opts = append(opts, ctxengine.WithMessageThreshold(*t.MessageCount))
		}
		if t.Days != nil {
			opts = append(opts, ctxengine.WithTriggerDays(*t.Days))
		}
	}
	if r := c.Retain; r != nil {
		if r.TokenPercent != nil {
			opts = append(opts, ctxengine.WithRetainTokenPercent(*r.TokenPercent))
		}
		if r.MaxTokens != nil {
			opts = append(opts, ctxengine.WithRetainMaxTokens(*r.MaxTokens))
		}
		if r.MaxAgeDays != nil {
			opts = append(opts, ctxengine.WithRetainMaxAgeDays(*r.MaxAgeDays))
		}
		if r.MinMessages != nil {
			opts = append(opts, ctxengine.WithRetainMinMessages(*r.MinMessages))
		}
	}
	if e := c.Estimate; e != nil {
		if e.CharsPerToken != nil {
			opts = append(opts, ctxengine.WithCharsPerToken(*e.CharsPerToken))
		}
		if e.TokenSafetyMargin != nil {
			opts = append(opts, ctxengine.WithTokenSafetyMargin(*e.TokenSafetyMargin))
		}
	}
	return opts
}

// registryOwnerName is the name refusals use for the agent. A config agent
// (and any agent with a name) is its display name. An unnamed clone acts as
// its source, so it is the source's display name rather than the clone's
// UUID; an unnamed fresh temporary agent is a short neutral label.
func registryOwnerName(spec agentreg.Spec, agentCfg *config.AgentConfig, cfg *config.Config) string {
	if strings.TrimSpace(agentCfg.Name) != "" {
		return agentCfg.Name
	}
	switch {
	case spec.IsClone():
		if src := cfg.AgentByID(spec.SourceID); src != nil {
			return src.DisplayName()
		}
		return spec.SourceID
	case spec.Fresh:
		return "temporary agent " + agentreg.ShortID(spec.ID)
	}
	return agentCfg.DisplayName()
}
