package files

import (
	"regexp"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the filesystem tools through the transport-neutral
// global layer with BARE names ("read", "write", "list", "edit", "append",
// "copy"). The aggregator mounts it under the "file" namespace, so the published
// names are "file_read" / "file_write" / etc. It reuses the existing tool logic
// (mirroring filesProvider.Build's construction exactly) and converts the result
// at the boundary, so behaviour is unchanged.
var GlobalProvider globalFilesProvider

type globalFilesProvider struct{}

// Namespace/Description/Available satisfy global.HostMeta.
func (globalFilesProvider) Namespace() string   { return "file" }
func (globalFilesProvider) Description() string { return "File read/write/list/edit operations" }

func (globalFilesProvider) Available(cfg any) (bool, string) { return true, "" }

func (globalFilesProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	// Construct the real tool instances only when real config is present.
	// Enumeration (Describe) passes a zero Deps; handlers are never called then,
	// so leaving the instances nil is safe.
	c, _ := deps.Cfg.(*config.Config)
	cd, _ := deps.Host.(tools.ToolDeps)

	var (
		read  *ReadFileTool
		write *WriteFileTool
		list  *ListDirTool
		edit  *EditFileTool
		apnd  *AppendFileTool
		cp    *CopyFileTool
	)

	if c != nil {
		// Mirror filesProvider.Build's construction logic exactly so behaviour
		// is preserved. The bridge + agent allowlist handle gating, so we
		// construct ALL six tools here (no IsToolEnabled / isToolAllowed gate).
		workspace := cd.Workspace
		agentCfg := cd.AgentCfg
		restrict := c.Agents.Defaults.RestrictToWorkspace
		readRestrict := restrict && !c.Agents.Defaults.AllowReadOutsideWorkspace

		allowReadPaths := compilePatterns(c.Tools.AllowReadPaths)
		allowWritePaths := compilePatterns(c.Tools.AllowWritePaths)

		// Always allow reading from the global skills directory.
		if skillsPath := c.SkillsPath(); skillsPath != "" {
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

		maxReadFileSize := c.Tools.ReadFile.MaxReadFileSize

		if memoryRedirectActive != "" {
			read = NewReadFileToolWithMemoryRedirect(workspace, readRestrict, maxReadFileSize, allowReadPaths, memoryRedirectActive)
			write = NewWriteFileToolWithMemoryRedirect(workspace, restrict, allowWritePaths, memoryRedirectActive)
			list = NewListDirToolWithMemoryRedirect(workspace, readRestrict, allowReadPaths, memoryRedirectActive)
			edit = NewEditFileToolWithMemoryRedirect(workspace, restrict, allowWritePaths, memoryRedirectActive)
			apnd = NewAppendFileToolWithMemoryRedirect(workspace, restrict, allowWritePaths, memoryRedirectActive)
			cp = NewCopyFileToolWithMemoryRedirect(workspace, restrict, allowWritePaths, memoryRedirectActive)
		} else {
			read = NewReadFileTool(workspace, readRestrict, maxReadFileSize, allowReadPaths)
			write = NewWriteFileTool(workspace, restrict, allowWritePaths)
			list = NewListDirTool(workspace, readRestrict, allowReadPaths)
			edit = NewEditFileTool(workspace, restrict, allowWritePaths)
			apnd = NewAppendFileTool(workspace, restrict, allowWritePaths)
			cp = NewCopyFileTool(workspace, restrict, allowWritePaths)
		}
	}

	return []global.ToolDefinition{
		{
			Name:         "read",
			Description:  (&ReadFileTool{}).Description(),
			RawSchema:    readSchema,
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(read.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:         "write",
			Description:  (&WriteFileTool{}).Description(),
			RawSchema:    (&WriteFileTool{}).Parameters(),
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(write.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:         "list",
			Description:  (&ListDirTool{}).Description(),
			RawSchema:    (&ListDirTool{}).Parameters(),
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(list.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:         "edit",
			Description:  (&EditFileTool{}).Description(),
			RawSchema:    (&EditFileTool{}).Parameters(),
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(edit.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:         "append",
			Description:  (&AppendFileTool{}).Description(),
			RawSchema:    (&AppendFileTool{}).Parameters(),
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(apnd.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:         "copy",
			Description:  (&CopyFileTool{}).Description(),
			RawSchema:    (&CopyFileTool{}).Parameters(),
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(cp.Execute(call.Ctx, call.Args)), nil
			},
		},
	}
}

// readSchema is the static JSON Schema for the read tool. ReadFileTool.Parameters()
// embeds the instance's maxSize as the "length" default, so a zero-value instance
// would report default 0; this literal uses MaxReadFileSize to match a properly
// constructed instance's default.
var readSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Path to the file to read.",
		},
		"offset": map[string]any{
			"type":        "integer",
			"description": "Byte offset to start reading from.",
			"default":     0,
		},
		"length": map[string]any{
			"type":        "integer",
			"description": "Maximum number of bytes to read.",
			"default":     int64(MaxReadFileSize),
		},
	},
	"required": []string{"path"},
}
