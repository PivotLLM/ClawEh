package files

import (
	"regexp"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// Provider is the singleton ToolProvider for filesystem tools.
var Provider filesProvider

type filesProvider struct{}

func (p filesProvider) Namespace() string   { return "files" }
func (p filesProvider) Description() string { return "File system read/write/list operations" }
func (p filesProvider) Category() string    { return "filesystem" }
func (p filesProvider) ConfigKey() string   { return "files" }

func (p filesProvider) Available(cfg *config.Config) (bool, string) {
	return true, ""
}

func (p filesProvider) Build(deps tools.ToolDeps) []tools.Tool {
	cfg := deps.Cfg
	if cfg == nil {
		return nil
	}
	workspace := deps.Workspace
	agentCfg := deps.AgentCfg
	restrict := cfg.Agents.Defaults.RestrictToWorkspace
	readRestrict := restrict && !cfg.Agents.Defaults.AllowReadOutsideWorkspace

	allowReadPaths := compilePatterns(cfg.Tools.AllowReadPaths)
	allowWritePaths := compilePatterns(cfg.Tools.AllowWritePaths)

	// Always allow reading from the global skills directory.
	if skillsPath := cfg.SkillsPath(); skillsPath != "" {
		if re, err := regexp.Compile("^" + regexp.QuoteMeta(skillsPath) + "/"); err == nil {
			allowReadPaths = append(allowReadPaths, re)
		}
	}

	// Resolve memory redirect.
	memoryDir := resolveMemoryDir(agentCfg)
	memoryRedirectActive := ""
	if memoryDir != "" {
		defaultMemDir := workspace + "/memory"
		if memoryDir != defaultMemDir {
			memoryRedirectActive = memoryDir
		}
	}

	var result []tools.Tool

	if cfg.Tools.IsToolEnabled("files_read") && isToolAllowed(agentCfg, "files_read") {
		maxReadFileSize := cfg.Tools.ReadFile.MaxReadFileSize
		if memoryRedirectActive != "" {
			result = append(result, NewReadFileToolWithMemoryRedirect(workspace, readRestrict, maxReadFileSize, allowReadPaths, memoryRedirectActive))
		} else {
			result = append(result, NewReadFileTool(workspace, readRestrict, maxReadFileSize, allowReadPaths))
		}
	}
	if cfg.Tools.IsToolEnabled("files_write") && isToolAllowed(agentCfg, "files_write") {
		if memoryRedirectActive != "" {
			result = append(result, NewWriteFileToolWithMemoryRedirect(workspace, restrict, allowWritePaths, memoryRedirectActive))
		} else {
			result = append(result, NewWriteFileTool(workspace, restrict, allowWritePaths))
		}
	}
	if cfg.Tools.IsToolEnabled("files_list") && isToolAllowed(agentCfg, "files_list") {
		if memoryRedirectActive != "" {
			result = append(result, NewListDirToolWithMemoryRedirect(workspace, readRestrict, allowReadPaths, memoryRedirectActive))
		} else {
			result = append(result, NewListDirTool(workspace, readRestrict, allowReadPaths))
		}
	}
	if cfg.Tools.IsToolEnabled("files_edit") && isToolAllowed(agentCfg, "files_edit") {
		if memoryRedirectActive != "" {
			result = append(result, NewEditFileToolWithMemoryRedirect(workspace, restrict, allowWritePaths, memoryRedirectActive))
		} else {
			result = append(result, NewEditFileTool(workspace, restrict, allowWritePaths))
		}
	}
	if cfg.Tools.IsToolEnabled("files_append") && isToolAllowed(agentCfg, "files_append") {
		if memoryRedirectActive != "" {
			result = append(result, NewAppendFileToolWithMemoryRedirect(workspace, restrict, allowWritePaths, memoryRedirectActive))
		} else {
			result = append(result, NewAppendFileTool(workspace, restrict, allowWritePaths))
		}
	}
	if cfg.Tools.IsToolEnabled("files_copy") && isToolAllowed(agentCfg, "files_copy") {
		if memoryRedirectActive != "" {
			result = append(result, NewCopyFileToolWithMemoryRedirect(workspace, restrict, allowWritePaths, memoryRedirectActive))
		} else {
			result = append(result, NewCopyFileTool(workspace, restrict, allowWritePaths))
		}
	}

	return result
}

func (p filesProvider) Describe() []tools.ToolDescriptor {
	return []tools.ToolDescriptor{
		{Name: "files_read", Description: "Read file content from the workspace or explicitly allowed paths.", Category: "filesystem", ConfigKey: "files_read", DefaultEnabled: true},
		{Name: "files_write", Description: "Create or overwrite files within the writable workspace scope.", Category: "filesystem", ConfigKey: "files_write", DefaultEnabled: true},
		{Name: "files_list", Description: "Inspect directories and enumerate files available to the agent.", Category: "filesystem", ConfigKey: "files_list", DefaultEnabled: true},
		{Name: "files_edit", Description: "Apply targeted edits to existing files without rewriting everything.", Category: "filesystem", ConfigKey: "files_edit", DefaultEnabled: true},
		{Name: "files_append", Description: "Append content to the end of an existing file.", Category: "filesystem", ConfigKey: "files_append", DefaultEnabled: true},
		{Name: "files_copy", Description: "Copy a file from a source path to a destination path within the workspace.", Category: "filesystem", ConfigKey: "files_copy", DefaultEnabled: true},
	}
}

// resolveMemoryDir returns the memory_dir override for the agent, or "".
func resolveMemoryDir(agentCfg *config.AgentConfig) string {
	if agentCfg == nil {
		return ""
	}
	return agentCfg.MemoryDir
}

// isToolAllowed checks whether the agent config permits the named tool.
func isToolAllowed(agentCfg *config.AgentConfig, name string) bool {
	if agentCfg == nil {
		return true
	}
	return agentCfg.IsToolAllowed(name)
}

// compilePatterns compiles regex patterns, silently skipping invalid ones.
func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}
