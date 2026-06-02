package agent

import (
	"context"
	"fmt"
	"log"
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

	// Router is non-nil when model routing is configured and the light model
	// was successfully resolved. It scores each incoming message and decides
	// whether to route to LightCandidates or stay with Candidates.
	Router *routing.Router
	// LightCandidates holds the resolved provider candidates for the light model.
	// Pre-computed at agent creation to avoid repeated model_list lookups at runtime.
	LightCandidates []providers.FallbackCandidate

	// Config is the agent's configuration, used for per-agent tool allowlists.
	Config *config.AgentConfig
}

// NewAgentInstance creates an agent instance from config.
func NewAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
) *AgentInstance {
	workspace := resolveAgentWorkspace(agentCfg, defaults)

	// Resolve the memory directory before populating templates so the workspace
	// seeder never recreates <workspace>/memory when memory is relocated.
	// Empty = legacy <workspace>/memory layout.
	memoryDir := resolveAgentMemoryDir(agentCfg)

	agentws.Populate(workspace, memoryDir)

	// When memory is relocated (non-empty and non-default), eagerly create the
	// directory: the memory tools open it via os.OpenRoot, which requires it to
	// exist. Failure is a hard startup error so we never silently fall back to
	// workspace memory and leak private notes into the wrong directory.
	if memoryDir != "" {
		defaultMem := filepath.Join(workspace, "memory")
		if memoryDir != defaultMem {
			if err := os.MkdirAll(memoryDir, 0o700); err != nil {
				log.Fatalf("agent %q: failed to create memory_dir %q: %v",
					func() string {
						if agentCfg != nil {
							return agentCfg.ID
						}
						return ""
					}(),
					memoryDir, err)
			}
		}
	}

	model := resolveAgentModel(agentCfg, defaults)
	fallbacks := resolveAgentFallbacks(agentCfg, defaults)

	restrict := defaults.RestrictToWorkspace
	_ = restrict // restrict is available to providers via cfg and defaults

	toolsRegistry := tools.NewToolRegistry()

	sessionsDir := filepath.Join(workspace, "sessions")
	sessions := initSessionStore(sessionsDir)

	// Phase 1: register tools via providers (files, shell, hardware, session history).
	// Runtime-dependent providers (session closures, spawn, msg) are registered
	// by the AgentLoop after construction via registerRuntimeTools().
	phase1Deps := tools.ToolDeps{
		Cfg:      cfg,
		AgentCfg: agentCfg,
		AgentID: func() string {
			if agentCfg != nil {
				return routing.NormalizeAgentID(agentCfg.ID)
			}
			return routing.DefaultAgentID
		}(),
		Workspace: workspace,
	}
	for _, p := range tools.GetProviders() {
		if ok, _ := p.Available(cfg); !ok {
			continue
		}
		builtTools := p.Build(phase1Deps)
		for _, t := range builtTools {
			if agentCfg == nil || agentCfg.IsToolAllowed(t.Name()) {
				toolsRegistry.Register(t)
			}
		}
	}

	mcpDiscoveryActive := cfg.Tools.MCP.Enabled && cfg.Tools.MCP.Discovery.Enabled
	contextBuilder := NewContextBuilderWithMemory(workspace, memoryDir).WithToolDiscovery(
		mcpDiscoveryActive && cfg.Tools.MCP.Discovery.UseBM25,
		mcpDiscoveryActive && cfg.Tools.MCP.Discovery.UseRegex,
	)
	// For named agents, always apply the skills filter — even if empty.
	// nil filter = no restriction (all skills); empty filter = no skills.
	// Default/nil agentCfg means the default agent which gets all skills.
	if agentCfg != nil && agentCfg.Skills != nil {
		contextBuilder = contextBuilder.WithSkillsFilter(agentCfg.Skills)
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
	resolveIntOpt := func(agentPtr *int, defaultsVal int) (int, bool) {
		if agentPtr != nil {
			return *agentPtr, true
		}
		if defaultsVal != 0 {
			return defaultsVal, true
		}
		return 0, false
	}

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
	// Resolve fallback candidates
	modelCfg := providers.ModelConfig{
		Primary:   model,
		Fallbacks: fallbacks,
	}
	resolveFromModelList := func(raw string) (alias, resolved string, ok bool) {
		ensureProtocol := func(model string) string {
			model = strings.TrimSpace(model)
			if model == "" {
				return ""
			}
			if strings.Contains(model, "/") {
				return model
			}
			return "openai/" + model
		}

		raw = strings.TrimSpace(raw)
		if raw == "" {
			return "", "", false
		}

		if cfg != nil {
			if mc, err := cfg.GetModelConfig(raw); err == nil && mc != nil && strings.TrimSpace(mc.Model) != "" {
				return mc.ModelName, ensureProtocol(mc.Model), true
			}

			for i := range cfg.ModelList {
				if !cfg.ModelList[i].Enabled {
					continue
				}
				fullModel := strings.TrimSpace(cfg.ModelList[i].Model)
				if fullModel == "" {
					continue
				}
				if fullModel == raw {
					return cfg.ModelList[i].ModelName, ensureProtocol(fullModel), true
				}
				_, modelID := providers.ExtractProtocol(fullModel)
				if modelID == raw {
					return cfg.ModelList[i].ModelName, ensureProtocol(fullModel), true
				}
			}
		}

		return "", "", false
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

	// Model routing setup: pre-resolve light model candidates at creation time
	// to avoid repeated model_list lookups on every incoming message.
	var router *routing.Router
	var lightCandidates []providers.FallbackCandidate
	if rc := defaults.Routing; rc != nil && rc.Enabled && rc.LightModel != "" {
		lightModelCfg := providers.ModelConfig{Primary: rc.LightModel}
		resolved := providers.ResolveCandidatesWithLookup(lightModelCfg, "", resolveFromModelList)
		if len(resolved) > 0 {
			router = routing.New(routing.RouterConfig{
				LightModel: rc.LightModel,
				Threshold:  rc.Threshold,
			})
			lightCandidates = resolved
		} else {
			log.Printf("routing: light_model %q not found in model_list — routing disabled for agent %q",
				rc.LightModel, agentID)
		}
	}

	// Normalize agentCfg to non-nil so Config is never nil after construction.
	// A nil config is equivalent to an empty allowlist (deny all tools).
	// IsToolAllowed() is already nil-safe, but callers should not need to guard on nil.
	if agentCfg == nil {
		agentCfg = &config.AgentConfig{Tools: []string{}}
	}

	return &AgentInstance{
		ID:              agentID,
		Name:            agentName,
		Model:           model,
		Fallbacks:       fallbacks,
		Workspace:       workspace,
		MaxIterations:   maxIter,
		MaxTokens:       maxTokens,
		Temperature:     temperature,
		ThinkingLevel:   thinkingLevel,
		NoTools:         noTools,
		ContextWindow:   contextWindow,
		CompressOpts:    compressOpts,
		Provider:        provider,
		Sessions:        sessions,
		ContextBuilder:  contextBuilder,
		Tools:           toolsRegistry,
		Subagents:       subagents,
		SkillsFilter:    skillsFilter,
		Candidates:      candidates,
		Router:          router,
		LightCandidates: lightCandidates,
		Config:          agentCfg,
	}
}

// resolveAgentMemoryDir returns the absolute on-disk memory directory override
// for an agent, or "" when the legacy <workspace>/memory layout should be used.
//
// Note on migration: relocating memory_dir does NOT auto-copy any files that
// already live under <workspace>/memory. Operators must move existing
// MEMORY.md and daily-note files manually; otherwise they become orphaned.
func resolveAgentMemoryDir(agentCfg *config.AgentConfig) string {
	if agentCfg == nil {
		return ""
	}
	md := strings.TrimSpace(agentCfg.MemoryDir)
	if md == "" {
		return ""
	}
	return expandHome(md)
}

// resolveAgentWorkspace determines the workspace directory for an agent.
func resolveAgentWorkspace(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) string {
	if agentCfg != nil && strings.TrimSpace(agentCfg.Workspace) != "" {
		return expandHome(strings.TrimSpace(agentCfg.Workspace))
	}
	// Use the configured default workspace (respects CLAW_HOME)
	if agentCfg == nil || agentCfg.ID == "" || routing.NormalizeAgentID(agentCfg.ID) == "main" {
		return expandHome(defaults.Workspace)
	}
	// For named agents without explicit workspace, use agents/{id} sibling of default
	id := routing.NormalizeAgentID(agentCfg.ID)
	agentsDir := filepath.Dir(expandHome(defaults.Workspace)) // ~/.claw/agents
	return filepath.Join(agentsDir, id)
}

// resolveAgentModel resolves the primary model for an agent.
func resolveAgentModel(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) string {
	if agentCfg != nil && agentCfg.Model != nil && strings.TrimSpace(agentCfg.Model.Primary) != "" {
		return strings.TrimSpace(agentCfg.Model.Primary)
	}
	return defaults.DefaultModelName()
}

// resolveAgentFallbacks resolves the fallback models for an agent.
func resolveAgentFallbacks(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) []string {
	if agentCfg != nil && agentCfg.Model != nil && agentCfg.Model.Fallbacks != nil {
		return agentCfg.Model.Fallbacks
	}
	if defaults.Model != nil {
		return defaults.Model.Fallbacks
	}
	return nil
}

func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			fmt.Printf("Warning: invalid path pattern %q: %v\n", p, err)
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
		log.Printf("memory: init store: %v; using json sessions", err)
		return session.NewSessionManager(dir)
	}

	if n, merr := memory.MigrateFromJSON(context.Background(), dir, store); merr != nil {
		// Migration failure means the store could not write data.
		// Fall back to SessionManager to avoid a split state where
		// some sessions are in JSONL and others remain in JSON.
		log.Printf("memory: migration failed: %v; falling back to json sessions", merr)
		store.Close()
		return session.NewSessionManager(dir)
	} else if n > 0 {
		log.Printf("memory: migrated %d session(s) to jsonl", n)
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
