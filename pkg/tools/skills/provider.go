package skills

import (
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
	skillspkg "github.com/PivotLLM/ClawEh/pkg/skills"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// Provider is the singleton ToolProvider for skills tools.
var Provider skillsProvider

type skillsProvider struct{}

func (p skillsProvider) Namespace() string   { return "skills" }
func (p skillsProvider) Description() string { return "Skill discovery and installation" }
func (p skillsProvider) Category() string    { return "skills" }
func (p skillsProvider) ConfigKey() string   { return "skills" }

func (p skillsProvider) Available(cfg *config.Config) (bool, string) {
	return true, ""
}

func (p skillsProvider) Build(deps tools.ToolDeps) []tools.Tool {
	cfg := deps.Cfg
	if cfg == nil {
		return nil
	}
	agentCfg := deps.AgentCfg

	registryEnabled := cfg.Tools.Skills.Registry.Enabled
	findEnabled := cfg.Tools.IsToolEnabled("skills_find") && isToolAllowed(agentCfg, "skills_find")
	installEnabled := cfg.Tools.IsToolEnabled("skills_install") && isToolAllowed(agentCfg, "skills_install")

	if !registryEnabled || (!findEnabled && !installEnabled) {
		return nil
	}

	registryMgr := skillspkg.NewRegistryManagerFromConfig(skillspkg.RegistryConfig{
		MaxConcurrentSearches: cfg.Tools.Skills.MaxConcurrentSearches,
		ClawHub:               skillspkg.ClawHubConfig(cfg.Tools.Skills.Registries.ClawHub),
	})

	var result []tools.Tool

	if findEnabled {
		searchCache := skillspkg.NewSearchCache(
			cfg.Tools.Skills.SearchCache.MaxSize,
			time.Duration(cfg.Tools.Skills.SearchCache.TTLSeconds)*time.Second,
		)
		result = append(result, NewFindSkillsTool(registryMgr, searchCache))
	}

	if installEnabled {
		result = append(result, NewInstallSkillTool(registryMgr, deps.Workspace))
	}

	return result
}

// isToolAllowed checks whether the agent config permits the named tool.
func isToolAllowed(agentCfg *config.AgentConfig, name string) bool {
	if agentCfg == nil {
		return true
	}
	return agentCfg.IsToolAllowed(name)
}
