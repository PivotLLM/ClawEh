package agent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	agentws "github.com/PivotLLM/ClawEh/internal/workspace"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/ClawEh/pkg/routing"
	"github.com/PivotLLM/ClawEh/pkg/session"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// AgentInstance represents a fully configured agent with its own workspace,
// session manager, context builder, and tool registry.
type AgentInstance struct {
	ID             string
	Name           string
	Model          string
	Fallbacks      []string
	Workspace      string
	MaxIterations  int
	MaxTokens      int
	Temperature    float64
	ThinkingLevel  ThinkingLevel
	NoTools        bool
	ContextWindow  int
	CompressOpts   []llmcontext.Option
	Provider       providers.LLMProvider
	Sessions       session.SessionStore
	ContextBuilder *ContextBuilder
	Tools          *tools.ToolRegistry
	Subagents      *config.SubagentsConfig
	SkillsFilter   []string
	Candidates     []providers.FallbackCandidate

	// Config is the agent's configuration, used for per-agent tool allowlists.
	Config *config.AgentConfig

	// DiscoveryActive is the effective progressive-discovery decision for this
	// agent (per-agent preference, overridden on by the auto_threshold). AgentLoop
	// sets it during tool registration; loop_mcp reads it to decide whether MCP
	// tools are registered hidden. When true, discovery-eligible tools (fusion and
	// maestro suites, all upstream MCP) are hidden behind search_tools /
	// get_tool_details; native tools and cogmem stay always-on.
	DiscoveryActive bool
}

// NewAgentInstance creates an agent instance from config.
func NewAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
) *AgentInstance {
	workspace := resolveAgentWorkspace(agentCfg, cfg.BaseDir())

	agentws.Populate(workspace)

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

	sessionsDir := filepath.Join(workspace, "sessions")
	sessions := initSessionStore(sessionsDir)

	// The registry starts empty. Tools are registered exactly once — after
	// construction by AgentLoop.registerRuntimeTools, and again on config reload —
	// so the full runtime deps (session closures, the sub-agent spawner, and the
	// shared message tool) are present. Registering here too would double-build
	// every tool and overwrite it, so we intentionally don't.

	// Progressive discovery is a single global switch; AgentLoop also sets it during
	// tool registration (and DiscoveryActive), so this just seeds the context rule.
	contextBuilder := NewContextBuilder(workspace).WithToolDiscovery(cfg.Tools.Discovery.Enabled)
	// For named agents, always apply the skills filter — even if empty.
	// nil filter = no restriction (all skills); empty filter = no skills.
	// Default/nil agentCfg means the default agent which gets all skills.
	if agentCfg != nil && agentCfg.Skills != nil {
		contextBuilder = contextBuilder.WithSkillsFilter(agentCfg.Skills)
	}
	if agentCfg != nil && len(agentCfg.Mounts) > 0 {
		contextBuilder = contextBuilder.WithMounts(agentCfg.Mounts)
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

	// resolveFloatOpt mirrors resolveIntOpt for float-valued knobs. 0 = not
	// configured (use the llmcontext default).
	resolveFloatOpt := func(agentPtr *float64, defaultsVal float64) (float64, bool) {
		if agentPtr != nil {
			return *agentPtr, true
		}
		if defaultsVal != 0 {
			return defaultsVal, true
		}
		return 0, false
	}

	var compressOpts []llmcontext.Option

	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.CompressMinPercent
		}
		return nil
	}(), defaults.CompressMinPercent); ok {
		compressOpts = append(compressOpts, llmcontext.WithMinPercent(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.CompressNormalPercent
		}
		return nil
	}(), defaults.CompressNormalPercent); ok {
		compressOpts = append(compressOpts, llmcontext.WithNormalPercent(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.CompressSafetyPercent
		}
		return nil
	}(), defaults.CompressSafetyPercent); ok {
		compressOpts = append(compressOpts, llmcontext.WithSafetyPercent(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.CompressMessageThreshold
		}
		return nil
	}(), defaults.CompressMessageThreshold); ok {
		compressOpts = append(compressOpts, llmcontext.WithMessageThreshold(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.CompressRetainTokenPercent
		}
		return nil
	}(), defaults.CompressRetainTokenPercent); ok {
		compressOpts = append(compressOpts, llmcontext.WithRetainTokenPercent(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.CompressRetainMinMessages
		}
		return nil
	}(), defaults.CompressRetainMinMessages); ok {
		compressOpts = append(compressOpts, llmcontext.WithRetainMinMessages(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.ArchiveMessageCount
		}
		return nil
	}(), defaults.ArchiveMessageCount); ok {
		compressOpts = append(compressOpts, llmcontext.WithArchiveMessageCount(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.ArchiveDays
		}
		return nil
	}(), defaults.ArchiveDays); ok {
		compressOpts = append(compressOpts, llmcontext.WithArchiveDays(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.SummaryMaxCount
		}
		return nil
	}(), defaults.SummaryMaxCount); ok {
		compressOpts = append(compressOpts, llmcontext.WithSummaryMaxCount(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.SummaryRetentionDays
		}
		return nil
	}(), defaults.SummaryRetentionDays); ok {
		compressOpts = append(compressOpts, llmcontext.WithSummaryRetentionDays(v))
	}
	if v, ok := resolveFloatOpt(func() *float64 {
		if agentCfg != nil {
			return agentCfg.CompressCharsPerToken
		}
		return nil
	}(), defaults.CompressCharsPerToken); ok {
		compressOpts = append(compressOpts, llmcontext.WithCharsPerToken(v))
	}
	if v, ok := resolveFloatOpt(func() *float64 {
		if agentCfg != nil {
			return agentCfg.CompressTokenSafetyMargin
		}
		return nil
	}(), defaults.CompressTokenSafetyMargin); ok {
		compressOpts = append(compressOpts, llmcontext.WithTokenSafetyMargin(v))
	}
	if v, ok := resolveIntOpt(func() *int {
		if agentCfg != nil {
			return agentCfg.ArchiveContentMaxBytes
		}
		return nil
	}(), defaults.ArchiveContentMaxBytes); ok {
		compressOpts = append(compressOpts, llmcontext.WithArchiveContentMaxBytes(v))
	}

	// Resolve the per-turn eviction policy: built-in defaults, overlaid by the
	// defaults config block, overlaid by the per-agent block (field by field).
	evPolicy := llmcontext.DefaultEvictionPolicy()
	applyEvictionConfig(&evPolicy, defaults.ContextEviction)
	if agentCfg != nil {
		applyEvictionConfig(&evPolicy, agentCfg.ContextEviction)
	}
	compressOpts = append(compressOpts, llmcontext.WithEvictionPolicy(evPolicy))

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

	return &AgentInstance{
		ID:             agentID,
		Name:           agentName,
		Model:          model,
		Fallbacks:      fallbacks,
		Workspace:      workspace,
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
	}
}

// resolveAgentWorkspace determines the workspace directory for an agent:
// an explicit per-agent workspace wins; otherwise the agent lives at
// <base_dir>/<id>, with the routing-default agent (empty/"main" id) at
// <base_dir>/default.
func resolveAgentWorkspace(agentCfg *config.AgentConfig, baseDir string) string {
	if agentCfg != nil && strings.TrimSpace(agentCfg.Workspace) != "" {
		return expandHome(strings.TrimSpace(agentCfg.Workspace))
	}
	id := "default"
	if agentCfg != nil {
		if nid := routing.NormalizeAgentID(agentCfg.ID); nid != "" && nid != "main" {
			id = nid
		}
	}
	return filepath.Join(baseDir, id)
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

func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			logger.WarnCF("agent", "invalid path pattern",
				map[string]any{
					"pattern": p,
					"error":   err.Error(),
				})
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}

// Close releases resources held by the agent's session store.
func (a *AgentInstance) Close() error {
	if a.Sessions != nil {
		return a.Sessions.Close()
	}
	return nil
}

// initSessionStore creates the session persistence backend.
// It uses the JSONL store by default and auto-migrates legacy JSON sessions.
// Falls back to SessionManager if the JSONL store cannot be initialized or
// if migration fails (which indicates the store cannot write reliably).
func initSessionStore(dir string) session.SessionStore {
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		logger.WarnCF("memory", "init store failed; using json sessions",
			map[string]any{"error": err.Error()})
		return session.NewSessionManager(dir)
	}

	if n, merr := memory.MigrateFromJSON(context.Background(), dir, store); merr != nil {
		// Migration failure means the store could not write data.
		// Fall back to SessionManager to avoid a split state where
		// some sessions are in JSONL and others remain in JSON.
		logger.WarnCF("memory", "migration failed; falling back to json sessions",
			map[string]any{"error": merr.Error()})
		store.Close()
		return session.NewSessionManager(dir)
	} else if n > 0 {
		logger.InfoCF("memory", "migrated sessions to jsonl",
			map[string]any{"count": n})
	}

	return session.NewJSONLBackend(store)
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(path) > 1 && path[1] == '/' {
			return home + path[1:]
		}
		return home
	}
	return path
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
func applyEvictionConfig(p *llmcontext.EvictionPolicy, c *config.ContextEvictionConfig) {
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
}
