package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/cogmem"
	"github.com/PivotLLM/ctxengine"
	"github.com/PivotLLM/ctxengine/session"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
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

// DisplayName is the agent's name, trimmed, or its id when it has none
// (config.AgentConfig.DisplayName).
func (a *AgentInstance) DisplayName() string {
	return (&config.AgentConfig{ID: a.ID, Name: a.Name}).DisplayName()
}

// sessionsDirName is the directory under an agent's state directory that
// holds its conversation archives.
const sessionsDirName = "sessions"

// SessionsDir is the directory holding the agent's conversation archives.
func (a *AgentInstance) SessionsDir() string {
	return filepath.Join(a.StateDir, sessionsDirName)
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

	humanModel, agentCfg := humanAgentConfig(cfg, agentCfg)

	if err := prepareWorkspace(spec); err != nil {
		return nil, err
	}

	models := resolveAgentModels(agentCfg, defaults)
	model := ""
	var fallbacks []string
	if len(models) > 0 {
		model = models[0]
		fallbacks = models[1:]
	}

	migrateCognitiveMemory(spec, agentCfg)
	sessions, err := initSessionStore(filepath.Join(stateDir, sessionsDirName))
	if err != nil {
		return nil, err
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
	limits := resolveRunLimits(defaults, agentCfg, cfg, model)
	// Built from the agent's config as given, before a nil config becomes the
	// empty one below: a nil config has no cognitive memory, so no memory
	// guidance, while an empty one has it by default.
	contextBuilder := newAgentContextBuilder(spec, agentCfg, cfg)
	compressOpts := agentCompressOptions(agentCfg, defaults)

	candidates := resolveAgentCandidates(cfg, models, agentID)

	// Config is never nil after construction: a nil config is equivalent to
	// an empty allowlist (deny all tools), so callers need not guard on nil.
	if agentCfg == nil {
		agentCfg = &config.AgentConfig{Tools: []string{}}
	}
	if spec.ID == "" {
		spec.ID, spec.Config = agentID, agentCfg
	}

	// The tool registry starts empty. Tools are registered exactly once, after
	// construction by AgentLoop.agentBuilder (and again on config reload), so
	// the full runtime deps (session closures, the sub-agent spawner, the
	// per-agent message tool) are present.
	return &AgentInstance{
		ID:             agentID,
		Name:           agentName,
		Model:          model,
		Fallbacks:      fallbacks,
		Workspace:      workspace,
		StateDir:       stateDir,
		Spec:           spec,
		MaxIterations:  limits.maxIterations,
		MaxTokens:      limits.maxTokens,
		Temperature:    limits.temperature,
		ThinkingLevel:  limits.thinkingLevel,
		NoTools:        limits.noTools,
		ContextWindow:  limits.contextWindow,
		CompressOpts:   compressOpts,
		Provider:       provider,
		Sessions:       sessions,
		ContextBuilder: contextBuilder,
		Tools:          tools.NewToolRegistry(),
		Subagents:      subagents,
		SkillsFilter:   skillsFilter,
		Candidates:     candidates,
		Config:         agentCfg,
		HumanModel:     humanModel,
	}, nil
}

// humanAgentConfig returns the human model of a human agent and the config
// it runs with. A person's conversation is never given to a model, so a
// human agent has no cognitive memory to observe into or consolidate.
func humanAgentConfig(cfg *config.Config, agentCfg *config.AgentConfig) (string, *config.AgentConfig) {
	humanModel, human := cfg.HumanModelOf(agentCfg)
	if !human {
		return humanModel, agentCfg
	}
	c := *agentCfg
	off := false
	c.Cogmem = &off
	return humanModel, &c
}

// resolveAgentCandidates resolves the agent's models into its fallback
// chain; an empty chain is logged as an error.
func resolveAgentCandidates(cfg *config.Config, models []string, agentID string) []providers.FallbackCandidate {
	candidates := providers.ResolveCandidatesWithLookup(providers.ModelConfig{Models: models}, "", modelListLookup(cfg))
	if len(candidates) == 0 {
		var primary string
		var fallbacks []string
		if len(models) > 0 {
			primary, fallbacks = models[0], models[1:]
		}
		logger.ErrorCF("agent", "agent fallback chain is empty after resolving aliases",
			map[string]any{
				"agent_id":  agentID,
				"primary":   primary,
				"fallbacks": fallbacks,
			})
	}
	return candidates
}

// prepareWorkspace makes the agent's workspace ready: seeded with the prompt
// files and skills, except for a fresh temporary agent, whose prompt is
// entirely its creator's and whose workspace is only created.
func prepareWorkspace(spec agentreg.Spec) error {
	if !spec.Fresh {
		agentws.Populate(spec.Workspace)
		return nil
	}
	if err := os.MkdirAll(spec.Workspace, 0o700); err != nil {
		return fmt.Errorf("create workspace %s: %w", spec.Workspace, err)
	}
	return nil
}

// migrateCognitiveMemory brings the agent's cognitive memory to the current
// layout and schema now, rather than whenever it next happens to be opened.
// Lazy migration spreads a schema change across hours of ordinary use with
// no point an operator can call it done, and leaves the store of an agent
// nobody talks to that day on the old schema indefinitely. A fresh agent
// without memory never gets a memory directory.
func migrateCognitiveMemory(spec agentreg.Spec, agentCfg *config.AgentConfig) {
	migrateID := routing.DefaultAgentID
	if agentCfg != nil && agentCfg.ID != "" {
		migrateID = agentCfg.ID
	}
	if spec.Origin == agentreg.OriginTemp {
		migrateID = spec.Label()
	}
	if !spec.Fresh || agentCfg.CognitiveMemoryEnabled() {
		cogmemhost.Migrate(migrateID, spec.StateDir)
	}
}

// newAgentContextBuilder builds the agent's system prompt builder.
// Progressive discovery is a single global switch; AgentLoop also sets it
// during tool registration, so this only seeds the context rule.
func newAgentContextBuilder(spec agentreg.Spec, agentCfg *config.AgentConfig, cfg *config.Config) *ContextBuilder {
	contextBuilder := NewContextBuilder(spec.Workspace).WithToolDiscovery(cfg.Tools.Discovery.Enabled)
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
		if mounts := agentCfg.EffectiveMounts(spec.Workspace); len(mounts) > 0 {
			contextBuilder = contextBuilder.WithMounts(mounts)
		}
		contextBuilder = contextBuilder.WithMaestro(agentCfg.MaestroEnabled())
	}
	return contextBuilder
}

// runLimits are the per-request limits an agent runs with.
type runLimits struct {
	maxIterations int
	maxTokens     int
	temperature   float64
	contextWindow int
	thinkingLevel ThinkingLevel
	noTools       bool
}

// resolveRunLimits resolves the agent's limits: the model's settings over
// the agent's over the defaults, with built-in fallbacks (20 iterations,
// 8192 tokens, a 128000-token context window).
func resolveRunLimits(defaults *config.AgentDefaults, agentCfg *config.AgentConfig, cfg *config.Config, model string) runLimits {
	l := runLimits{
		maxIterations: defaults.MaxToolIterations,
		maxTokens:     defaults.MaxTokens,
		temperature:   global.DefaultTemperature,
		contextWindow: defaults.ContextWindow,
	}
	if l.maxIterations == 0 {
		l.maxIterations = 20
	}
	if l.maxTokens == 0 {
		l.maxTokens = 8192
	}
	if defaults.Temperature != nil {
		l.temperature = *defaults.Temperature
	}
	if agentCfg != nil && agentCfg.Temperature != nil {
		l.temperature = *agentCfg.Temperature
	}
	if l.contextWindow == 0 {
		l.contextWindow = 128000
	}

	var thinkingLevelStr string
	if mc, err := cfg.GetModelConfig(model); err == nil {
		thinkingLevelStr = mc.ThinkingLevel
		l.noTools = mc.NoTools
		if mc.ContextWindow > 0 {
			l.contextWindow = mc.ContextWindow
		}
		if mc.MaxTokens > 0 {
			l.maxTokens = mc.MaxTokens
		}
	}
	l.thinkingLevel = parseThinkingLevel(thinkingLevelStr)
	return l
}

// agentCompressOptions are the context engine options for the agent: the
// compaction policy (the defaults block overlaid by the per-agent block,
// only the fields the merged config sets), the archive and summary limits,
// and the per-turn eviction policy (built-in defaults, overlaid by the
// defaults block, overlaid by the per-agent block, field by field).
func agentCompressOptions(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) []ctxengine.Option {
	compressOpts := compressionOptions(agentCfg.EffectiveCompression(defaults.Compression))

	// For count fields 0 means explicitly disabled, which is valid to pass.
	agentInt := func(get func(*config.AgentConfig) *int) *int {
		if agentCfg != nil {
			return get(agentCfg)
		}
		return nil
	}
	for _, o := range []struct {
		agent    *int
		defaults int
		option   func(int) ctxengine.Option
	}{
		{agentInt(func(c *config.AgentConfig) *int { return c.ArchiveMessageCount }), defaults.ArchiveMessageCount, ctxengine.WithArchiveMessageCount},
		{agentInt(func(c *config.AgentConfig) *int { return c.ArchiveDays }), defaults.ArchiveDays, ctxengine.WithArchiveDays},
		{agentInt(func(c *config.AgentConfig) *int { return c.SummaryMaxCount }), defaults.SummaryMaxCount, ctxengine.WithSummaryMaxCount},
		{agentInt(func(c *config.AgentConfig) *int { return c.SummaryRetentionDays }), defaults.SummaryRetentionDays, ctxengine.WithSummaryRetentionDays},
		{agentInt(func(c *config.AgentConfig) *int { return c.ArchiveContentMaxBytes }), defaults.ArchiveContentMaxBytes, ctxengine.WithArchiveContentMaxBytes},
	} {
		if v, ok := resolveAgentIntOpt(o.agent, o.defaults); ok {
			compressOpts = append(compressOpts, o.option(v))
		}
	}

	evPolicy := ctxengine.DefaultEvictionPolicy()
	applyEvictionConfig(&evPolicy, defaults.ContextEviction)
	if agentCfg != nil {
		applyEvictionConfig(&evPolicy, agentCfg.ContextEviction)
	}
	return append(compressOpts, ctxengine.WithEvictionPolicy(evPolicy))
}

// modelListLookup resolves a model reference for the fallback chain: by
// model_name alias first, then by raw model id among the enabled models.
func modelListLookup(cfg *config.Config) func(raw string) (alias, model, provider string, ok bool) {
	return func(raw string) (alias, model, provider string, ok bool) {
		raw = strings.TrimSpace(raw)
		if raw == "" || cfg == nil {
			return "", "", "", false
		}
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
	store, err := session.NewSQLiteStore(dir)
	if err != nil {
		return nil, fmt.Errorf("open session store %s: %w", dir, err)
	}
	return store, nil
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
