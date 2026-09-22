// ClawEh
// License: MIT

package audit

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/config"
)

// sensitiveTools are the internal tools that reach beyond the agent's own
// workspace read: run programs, change files, send messages, install code,
// or start other agents.
var sensitiveTools = []string{"shell_exec", "file_write", "file_edit", "file_delete", "msg_send", "skill_install", "agent_spawn"}

// folderRow is one line of the Folder access table before sorting.
type folderRow struct {
	path      string
	read      bool
	write     bool
	notes     []string
	highlight bool
}

// folderAccess collects every path an agent can reach through ClawEh's file
// tools, plus the directories ClawEh itself writes on the agent's behalf,
// under the rule in tools/files (ReadPolicy and buildBaseFs): writes are
// confined when restrict_to_workspace is on, reads when it is on and
// allow_read_outside_workspace is off; allow-listed host patterns bypass both.
type folderAccess struct {
	rows map[string]*folderRow
	// special is the one highlighted "anything" row, kept first.
	special *folderRow
}

func (f *folderAccess) add(path string, read, write bool, note string) {
	r, ok := f.rows[path]
	if !ok {
		r = &folderRow{path: path}
		f.rows[path] = r
	}
	r.read = r.read || read
	r.write = r.write || write
	if note != "" {
		r.notes = append(r.notes, note)
	}
}

func agentFolderAccess(cfg *config.Config, env Environment, a *config.AgentConfig) Table {
	d := cfg.Agents.Defaults
	ws := resolveWorkspace(cfg, a)
	fa := &folderAccess{rows: map[string]*folderRow{}}
	user := orValue(env.User, unknown)

	switch {
	case !d.RestrictToWorkspace:
		fa.special = &folderRow{
			path: "anything user " + user + " can access", read: true, write: true, highlight: true,
			notes: []string{"restrict_to_workspace is off"},
		}
		fa.add(ws, true, true, "agent workspace")
	default:
		writeRoot := ws
		note := "workspace write area"
		if d.WorkspaceWriteSubdir != "" {
			writeRoot = filepath.Join(ws, d.WorkspaceWriteSubdir)
		} else {
			note = "whole workspace writable (workspace_write_subdir is empty)"
		}
		fa.add(writeRoot, false, true, note)
		if d.AllowReadOutsideWorkspace {
			fa.special = &folderRow{
				path: "anything user " + user + " can read", read: true, highlight: true,
				notes: []string{"allow_read_outside_workspace is on"},
			}
		} else if len(d.WorkspaceReadSubdirs) == 0 {
			fa.add(ws, true, false, "whole workspace readable (workspace_read_subdirs is empty)")
		} else {
			for _, sub := range d.WorkspaceReadSubdirs {
				fa.add(filepath.Join(ws, sub), true, false, "workspace read area")
			}
			fa.add(filepath.Join(ws, "tasks"), true, false, "sub-agent results (always readable)")
			fa.add(filepath.Join(ws, "tmp"), true, false, "inbound attachments (always readable)")
		}
		for _, p := range cfg.Tools.AllowReadPaths {
			fa.add(p, true, false, "pattern (allow_read_paths)")
		}
		for _, p := range cfg.Tools.AllowWritePaths {
			fa.add(p, false, true, "pattern (allow_write_paths)")
		}
	}

	for _, m := range a.EffectiveMounts(ws) {
		note := "mount " + m.Name
		if strings.EqualFold(m.Name, config.MaestroMountName) && m.Path == config.MaestroDataDir(ws) {
			note = "Maestro data (auto mount)"
		}
		if m.Notify {
			note += ", notify"
		}
		fa.add(m.Path, !m.Writable, m.Writable, note)
	}
	fa.add(cfg.SkillsPath(), true, false, "global skills")
	if a.SharesCommon() {
		fa.add(cfg.ResolveCommonDir(), true, true, "shared common directory (common_* tools)")
	}
	if a.CognitiveMemoryEnabled() {
		fa.add(cogmemhost.Dir(ws), false, true, "cognitive memory store (written by ClawEh, not via file tools)")
	}
	fa.add(filepath.Join(ws, "sessions"), false, true, "session archive (written by ClawEh, not via file tools)")

	t := Table{Caption: "Folder access", Columns: []string{"Path", "Access", "Notes"}}
	if fa.special != nil {
		t.Rows = append(t.Rows, row(fa.special.path, access(fa.special.read, fa.special.write), strings.Join(fa.special.notes, "; ")))
		t.Highlight = append(t.Highlight, 0)
	}
	for _, p := range sortedKeys(fa.rows) {
		r := fa.rows[p]
		shown, note := p, ""
		if !strings.Contains(strings.Join(r.notes, ""), "pattern") {
			shown, note = resolvePath(p)
		}
		notes := append([]string{}, r.notes...)
		if note != "" {
			notes = append(notes, note)
		}
		t.Rows = append(t.Rows, row(shown, access(r.read, r.write), strings.Join(notes, "; ")))
	}
	return t
}

func access(read, write bool) string {
	switch {
	case read && write:
		return "[read/write]"
	case write:
		return "[write]"
	default:
		return "[read]"
	}
}

// agentToolsTable lists the internal tools two per row, each with a yes/no
// sensitive marker, so the table uses the page width.
func agentToolsTable(a *config.AgentConfig) Table {
	t := Table{Caption: "Internal tools", Columns: []string{"Tool", "Sensitive", "Tool", "Sensitive"}}
	tools := effectiveTools(a)
	if len(tools) == 0 {
		t.Rows = append(t.Rows, row("(none: the tools list is empty)", "", "", ""))
		return t
	}
	var cells [][]string
	explicit := map[string]bool{}
	for _, e := range tools {
		explicit[strings.ToLower(e)] = true
		cells = append(cells, row(e, yesNo(isSensitive(e))))
	}
	for _, s := range sensitiveTools {
		if !explicit[s] && config.MatchToolPattern(tools, s) {
			cells = append(cells, row(s, "yes (by pattern)"))
		}
	}
	for i := 0; i < len(cells); i += 2 {
		r := row(cells[i][0], cells[i][1], "", "")
		if i+1 < len(cells) {
			r[2], r[3] = cells[i+1][0], cells[i+1][1]
		}
		t.Rows = append(t.Rows, r)
	}
	return t
}

func isSensitive(name string) bool {
	return slices.Contains(sensitiveTools, strings.ToLower(name))
}

func agentMCPTable(cfg *config.Config, a *config.AgentConfig) Table {
	t := Table{Caption: "MCP access", Columns: []string{"mcp_tools entry", "Servers reached"}}
	if len(a.MCPTools) == 0 {
		t.Rows = append(t.Rows, row(none, "no MCP tools"))
		return t
	}
	servers := sortedKeys(cfg.Tools.MCP.Servers)
	for _, e := range a.MCPTools {
		var reach []string
		for _, s := range servers {
			if desc, ok := mcpEntryReach(e, s); ok {
				reach = append(reach, s+" ("+desc+")")
			}
		}
		if len(reach) == 0 {
			t.Highlight = append(t.Highlight, len(t.Rows))
			t.Rows = append(t.Rows, row(e, "matches no configured server"))
			continue
		}
		t.Rows = append(t.Rows, row(e, strings.Join(reach, "; ")))
	}
	return t
}

func agentSection(cfg *config.Config, env Environment, a *config.AgentConfig) Section {
	d := cfg.Agents.Defaults
	models := a.Models
	modelsNote := ""
	if len(models) == 0 {
		models = d.Models
		modelsNote = " (from defaults)"
	}
	sub := "none allowed"
	if a.Subagents != nil && (len(a.Subagents.AllowAgents) > 0 || len(a.Subagents.Models) > 0) {
		sub = "agents " + joinOr(a.Subagents.AllowAgents, none) + "; models " + joinOr(a.Subagents.Models, "(agent's own)")
	}
	sub += "; gate " + onOff(cfg.Tools.Subagent.Enabled) + ", max depth " + itoa(d.GetMaxSubagentDepth())
	memory := onOff(a.CognitiveMemoryEnabled())
	if a.Memory != nil {
		memory += " (per-agent memory block)"
	}
	title := a.ID
	if a.Name != "" && a.Name != a.ID {
		title += " (" + a.Name + ")"
	}
	settings := pairs("Settings",
		row("Enabled", yesNo(a.IsEnabled())),
		row("Routing default", yesNo(strings.EqualFold(a.ID, defaultAgentID(cfg)))),
		row("Workspace", resolveWorkspace(cfg, a)),
		row("Models", strings.TrimSpace(joinLines(models, none)+"\n"+strings.TrimSpace(modelsNote))),
		row("Summarization models", joinOr(a.SummarizationModels, "(global chain)")),
		row("Cognitive memory", memory),
		row("Sub-agents", sub),
		row("Suites", "Maestro "+onOff(a.MaestroEnabled())+", Fusion "+onOff(a.Fusion)),
		row("Shared common directory", onOff(a.SharesCommon())),
		row("Global cron (schedules for other agents)", onOff(a.GlobalCron)),
	)
	return Section{
		Title:  title,
		Tables: []Table{settings, agentToolsTable(a), agentMCPTable(cfg, a), agentFolderAccess(cfg, env, a)},
	}
}

func collectAgents(_ context.Context, cfg *config.Config, env Environment) Section {
	d := cfg.Agents.Defaults
	readSubdirs := append([]string{}, d.WorkspaceReadSubdirs...)
	if len(readSubdirs) > 0 {
		readSubdirs = append(readSubdirs, "tasks", "tmp")
	}
	defaults := pairs("Defaults (agents.defaults)",
		row("restrict_to_workspace", onOff(d.RestrictToWorkspace)),
		row("allow_read_outside_workspace", onOff(d.AllowReadOutsideWorkspace)),
		row("Workspace write area", orValue(d.WorkspaceWriteSubdir, "(whole workspace)")),
		row("Workspace read areas", joinOr(readSubdirs, "(whole workspace)")),
		row("Agents base directory", cfg.BaseDir()),
		row("Default tool allowlist (when an agent sets none)", joinOr(config.DefaultAgentTools, none)),
		row("Max sub-agent depth", itoa(d.GetMaxSubagentDepth())),
	)
	s := Section{
		Title: "Agents",
		Notes: []string{
			"One subsection per configured agent. Internal tools marked sensitive run programs, change files, " +
				"send messages, install code or start other agents. Folder access lists every path the agent's " +
				"file tools can reach, resolved as the filesystem sees it, plus the directories ClawEh writes for the agent.",
		},
		Tables: []Table{defaults},
	}
	agents := make([]*config.AgentConfig, 0, len(cfg.Agents.List))
	for i := range cfg.Agents.List {
		agents = append(agents, &cfg.Agents.List[i])
	}
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	for _, a := range agents {
		s.Subsections = append(s.Subsections, agentSection(cfg, env, a))
	}
	if len(agents) == 0 {
		s.Notes = append(s.Notes, "No agents are configured.")
	}
	return s
}
