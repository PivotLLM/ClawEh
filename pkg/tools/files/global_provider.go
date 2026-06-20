package files

import (
	"os"
	"path/filepath"
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
		read   *ReadFileTool
		write  *WriteFileTool
		list   *ListDirTool
		edit   *EditFileTool
		apnd   *AppendFileTool
		cp     *CopyFileTool
		search *SearchFilesTool
	)

	if c != nil {
		// Mirror filesProvider.Build's construction logic exactly so behaviour
		// is preserved. The bridge + agent allowlist handle gating, so we
		// construct ALL six tools here (no IsToolEnabled / isToolAllowed gate).
		workspace := cd.Workspace
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

		maxReadFileSize := c.Tools.ReadFile.MaxReadFileSize

		// Writes are confined to <workspace>/<writeSubdir> (default "files") when
		// restriction is on; reads remain workspace-wide. Ensure the subdir exists
		// so the agent has somewhere to write.
		writeSubdir := c.Agents.Defaults.WorkspaceWriteSubdir
		if restrict && writeSubdir != "" && workspace != "" {
			_ = os.MkdirAll(filepath.Join(workspace, writeSubdir), 0o755)
		}

		// Confine agent reads to the configured workspace subdirs (default
		// files/ + skills/). Process-wide policy consulted by buildFs; empty
		// restores legacy workspace-wide reads. When a scope is active, always
		// include tasks/ — spawn callbacks point the agent at
		// tasks/<uuid>-results.json, so it must be readable regardless of the
		// configured read subdirs (read-only; writes stay confined to the write
		// subdir, so the agent cannot tamper with task state).
		if readRestrict {
			subdirs := c.Agents.Defaults.WorkspaceReadSubdirs
			if len(subdirs) > 0 {
				subdirs = appendIfMissing(subdirs, "tasks")
			}
			SetReadScopeSubdirs(subdirs)
		}

		read = NewReadFileTool(workspace, readRestrict, maxReadFileSize, allowReadPaths)
		search = NewSearchFilesTool(workspace, readRestrict, allowReadPaths)
		write = NewWriteFileToolScoped(workspace, restrict, writeSubdir, allowWritePaths)
		list = NewListDirTool(workspace, readRestrict, allowReadPaths)
		edit = NewEditFileToolScoped(workspace, restrict, writeSubdir, allowWritePaths)
		apnd = NewAppendFileToolScoped(workspace, restrict, writeSubdir, allowWritePaths)
		cp = NewCopyFileToolScoped(workspace, restrict, writeSubdir, allowWritePaths)
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
			Name:         "search",
			Description:  (&SearchFilesTool{}).Description(),
			RawSchema:    (&SearchFilesTool{}).Parameters(),
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if search == nil {
					return tools.ResultToGlobal(tools.ErrorResult("file search is not available")), nil
				}
				return tools.ResultToGlobal(search.Execute(call.Ctx, call.Args)), nil
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

// appendIfMissing returns subdirs with name appended if not already present.
func appendIfMissing(subdirs []string, name string) []string {
	for _, s := range subdirs {
		if s == name {
			return subdirs
		}
	}
	return append(append([]string(nil), subdirs...), name)
}
