package skills

import (
	"time"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	skillspkg "github.com/PivotLLM/ClawEh/pkg/skills"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the skill tools through the transport-neutral global
// layer with BARE names ("find", "install"). The aggregator mounts it under the
// "skill" namespace, so the published names are "skill_find" / "skill_install".
// It reuses the existing FindSkillsTool / InstallSkillTool logic and converts the
// result at the boundary, so behaviour is unchanged.
var GlobalProvider globalSkillProvider

type globalSkillProvider struct{}

// Namespace/Description/Available satisfy global.HostMeta. Available gates the
// whole package on the skills registry being enabled (the old package-level
// gate); when false the host reports the tools as blocked.
func (globalSkillProvider) Namespace() string   { return "skill" }
func (globalSkillProvider) Description() string { return "Skill discovery and installation" }

func (globalSkillProvider) Available(cfg any) (bool, string) {
	c, ok := cfg.(*config.Config)
	if !ok || c == nil {
		return false, "skills_registry_disabled"
	}
	if !c.Tools.Skills.Registry.Enabled {
		return false, "skills_registry_disabled"
	}
	return true, ""
}

func (globalSkillProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	// Construct the shared registry manager only when real config is present.
	// Enumeration (Describe) passes a zero Deps; handlers are never called then,
	// so leaving mgr/cache nil is safe.
	var mgr *skillspkg.RegistryManager
	var cache *skillspkg.SearchCache
	workspace := deps.Workspace
	if c, ok := deps.Cfg.(*config.Config); ok && c != nil {
		mgr = skillspkg.NewRegistryManagerFromConfig(skillspkg.RegistryConfig{
			MaxConcurrentSearches: c.Tools.Skills.MaxConcurrentSearches,
			ClawHub:               skillspkg.ClawHubConfig(c.Tools.Skills.Registries.ClawHub),
		})
		cache = skillspkg.NewSearchCache(
			c.Tools.Skills.SearchCache.MaxSize,
			time.Duration(c.Tools.Skills.SearchCache.TTLSeconds)*time.Second,
		)
	}

	find := NewFindSkillsTool(mgr, cache)
	install := NewInstallSkillTool(mgr, workspace)

	return []global.ToolDefinition{
		{
			Name:        "find",
			Description: "Search for installable skills from skill registries. Returns skill slugs, descriptions, versions, and relevance scores. Use this to discover skills before installing them with skill_install.",
			Parameters: []global.Parameter{
				{Name: "query", Type: "string", Required: true, Description: "Search query describing the desired skill capability (e.g., 'github integration', 'database management')"},
				{Name: "limit", Type: "integer", Description: "Maximum number of results to return (1-20, default 5)", Minimum: floatPtr(1), Maximum: floatPtr(20)},
			},
			Category: "skills",
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(find.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:        "install",
			Description: "Install a skill from a registry by slug. Downloads and extracts the skill into the workspace. Use skill_find first to discover available skills.",
			Parameters: []global.Parameter{
				{Name: "slug", Type: "string", Required: true, Description: "The unique slug of the skill to install (e.g., 'github', 'docker-compose')"},
				{Name: "registry", Type: "string", Required: true, Description: "Registry to install from (required, e.g., 'clawhub')"},
				{Name: "version", Type: "string", Description: "Specific version to install (optional, defaults to latest)"},
				{Name: "force", Type: "boolean", Description: "Force reinstall if skill already exists (default false)"},
			},
			Category: "skills",
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(install.Execute(call.Ctx, call.Args)), nil
			},
		},
	}
}

func floatPtr(v float64) *float64 { return &v }
