package files

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the filesystem tools through the transport-neutral
// global layer with BARE names ("read", "write", "list", "edit", "append",
// "copy"). The aggregator mounts it under the "file" namespace, so the published
// names are "file_read_bytes" / "file_write" / etc. It reuses the existing tool logic
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
		readBytes   *ReadFileTool
		readLines   *ReadFileTool
		write       *WriteFileTool
		list        *ListDirTool
		edit        *EditFileTool
		apnd        *AppendFileTool
		cp          *CopyFileTool
		del         *DeleteFileTool
		mv          *MoveFileTool
		searchLines *SearchFilesTool
		searchBytes *SearchFilesTool
		rangeTools  map[string]*rangeEditTool
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

		// External mounts (per agent): expose extra top-level dirs beside files/.
		// Must be registered before constructing the tools, which capture their
		// fileSystem (mount-aware) at build time.
		SetMountsForWorkspace(workspace, resolveAgentMounts(cd.AgentCfg))

		readBytes = NewReadFileTool(workspace, readRestrict, maxReadFileSize, allowReadPaths)
		readLines = NewReadLinesTool(workspace, readRestrict, maxReadFileSize, allowReadPaths)
		searchLines = NewSearchLinesTool(workspace, readRestrict, allowReadPaths)
		searchBytes = NewSearchBytesTool(workspace, readRestrict, allowReadPaths)
		write = NewWriteFileToolScoped(workspace, restrict, writeSubdir, allowWritePaths)
		list = NewListDirTool(workspace, readRestrict, allowReadPaths)
		edit = NewEditFileToolScoped(workspace, restrict, writeSubdir, allowWritePaths)
		apnd = NewAppendFileToolScoped(workspace, restrict, writeSubdir, allowWritePaths)
		cp = NewCopyFileToolScoped(workspace, restrict, writeSubdir, allowWritePaths)
		del = NewDeleteFileToolScoped(workspace, restrict, writeSubdir, allowWritePaths)
		mv = NewMoveFileToolScoped(workspace, restrict, writeSubdir, allowWritePaths)
		rangeTools = map[string]*rangeEditTool{}
		for _, op := range []string{"edit", "insert", "delete"} {
			for _, unit := range []string{"lines", "bytes"} {
				rangeTools[op+"_"+unit] = newRangeEditTool(op, unit, workspace, restrict, writeSubdir, allowWritePaths)
			}
		}
	}

	// rangeDef builds a ToolDefinition for one range-edit tool. The handler closes
	// over rangeTools[key]; schema/description use a config-free probe instance.
	rangeDef := func(op, unit string) global.ToolDefinition {
		key := op + "_" + unit
		probe := &rangeEditTool{op: op, unit: unit}
		return global.ToolDefinition{
			Name:         key, // namespace "file" → file_<op>_<unit>
			Description:  probe.Description(),
			RawSchema:    probe.Parameters(),
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				rt := rangeTools[key]
				if rt == nil {
					return tools.ResultToGlobal(tools.ErrorResult("file edit is not available")), nil
				}
				return tools.ResultToGlobal(rt.Execute(call.Ctx, call.Args)), nil
			},
		}
	}

	return []global.ToolDefinition{
		{
			Name:         "read_bytes",
			Description:  (&ReadFileTool{lineMode: false}).Description(),
			RawSchema:    readBytesSchema,
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(readBytes.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:         "read_lines",
			Description:  (&ReadFileTool{lineMode: true}).Description(),
			RawSchema:    readLinesSchema,
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(readLines.Execute(call.Ctx, call.Args)), nil
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
			Name:         "search_lines",
			Description:  (&SearchFilesTool{byteMode: false}).Description(),
			RawSchema:    (&SearchFilesTool{byteMode: false}).Parameters(),
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if searchLines == nil {
					return tools.ResultToGlobal(tools.ErrorResult("file search is not available")), nil
				}
				return tools.ResultToGlobal(searchLines.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:         "search_bytes",
			Description:  (&SearchFilesTool{byteMode: true}).Description(),
			RawSchema:    (&SearchFilesTool{byteMode: true}).Parameters(),
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if searchBytes == nil {
					return tools.ResultToGlobal(tools.ErrorResult("file search is not available")), nil
				}
				return tools.ResultToGlobal(searchBytes.Execute(call.Ctx, call.Args)), nil
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
		rangeDef("edit", "lines"),
		rangeDef("edit", "bytes"),
		rangeDef("insert", "lines"),
		rangeDef("insert", "bytes"),
		rangeDef("delete", "lines"),
		rangeDef("delete", "bytes"),
		{
			Name:         "delete",
			Description:  (&DeleteFileTool{}).Description(),
			RawSchema:    (&DeleteFileTool{}).Parameters(),
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(del.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:         "move",
			Description:  (&MoveFileTool{}).Description(),
			RawSchema:    (&MoveFileTool{}).Parameters(),
			Category:     "filesystem",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(mv.Execute(call.Ctx, call.Args)), nil
			},
		},
	}
}

// readBytesSchema is the static JSON Schema for file_read_bytes. A properly
// constructed ReadFileTool embeds its maxSize as the "length" default; this
// literal uses MaxReadFileSize to match that for enumeration (zero Deps).
var readBytesSchema = map[string]any{
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

// readLinesSchema is the static JSON Schema for file_read_lines.
var readLinesSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Path to the file to read.",
		},
		"start_line": map[string]any{
			"type":        "integer",
			"description": "1-based line to start from (default 1).",
			"default":     1,
		},
		"line_count": map[string]any{
			"type":        "integer",
			"description": "Number of lines to read from start_line (default 250). Still capped to the byte limit.",
			"default":     defaultReadLineCount,
		},
	},
	"required": []string{"path"},
}

// resolveAgentMounts validates an agent's mount config into usable specs. Invalid
// entries (bad name, missing/non-directory path, duplicate) are dropped with a
// WARN so one broken mount can't take the agent down.
func resolveAgentMounts(agentCfg *config.AgentConfig) []MountSpec {
	if agentCfg == nil || len(agentCfg.Mounts) == 0 {
		return nil
	}
	var specs []MountSpec
	seen := map[string]bool{}
	for _, mc := range agentCfg.Mounts {
		name := strings.TrimSpace(mc.Name)
		if err := config.ValidateMountName(name); err != nil {
			logger.WarnCF("tools", "skipping invalid mount", map[string]any{"name": name, "error": err.Error()})
			continue
		}
		if seen[name] {
			logger.WarnCF("tools", "skipping duplicate mount name", map[string]any{"name": name})
			continue
		}
		abs, err := filepath.Abs(strings.TrimSpace(mc.Path))
		if err != nil {
			logger.WarnCF("tools", "skipping mount: bad path", map[string]any{"name": name, "path": mc.Path})
			continue
		}
		info, serr := os.Stat(abs)
		if serr != nil || !info.IsDir() {
			logger.WarnCF("tools", "skipping mount: not an existing directory", map[string]any{"name": name, "path": abs})
			continue
		}
		seen[name] = true
		specs = append(specs, MountSpec{Name: name, Path: abs, Writable: mc.Writable})
	}
	return specs
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
