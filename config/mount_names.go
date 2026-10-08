// ClawEh
// License: MIT

package config

import (
	"errors"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/logger"
)

// ReservedWorkspaceNames are the folder names ClawEh creates or reads directly
// inside an agent workspace (or, for "common", publishes as a tool namespace
// beside them). A mount is reached at <name>/... in the same space, so a mount
// with one of these names, in any case, would shadow the folder: it is refused
// on save and ignored at load (IgnoredMounts).
//
//   - files:    the default write area (AgentDefaults.WorkspaceWriteSubdir)
//   - skills:   workspace skills (skills.NewSkillsLoader, the skills tools)
//   - tasks:    sub-agent results (tools/agents taskstore)
//   - tmp:      inbound attachments and transcriptions (agent loop)
//   - forums:   the agent's forums (WorkspaceForumsDir)
//   - maestro:  Maestro data (MaestroMountName)
//   - sessions: conversation archives (the session store)
//   - cogmem:   cognitive memory (cogmemhost.DirName)
//   - state:    in-flight turns kept for restart recovery (state.NewManager,
//     state.json) and the external-message tokens (message-tokens.json)
//   - common:   the shared directory's tools namespace
var ReservedWorkspaceNames = []string{
	"files", "skills", "tasks", "tmp", WorkspaceForumsDir, MaestroMountName,
	"sessions", "cogmem", "state", "common",
}

// WorkspaceForumsDir is the folder in an agent's workspace that holds its
// forums (the forum tools' base directory).
const WorkspaceForumsDir = "forums"

// AlwaysReadableWorkspaceDirs are the workspace folders an agent may always
// read when workspace_read_subdirs limits its reads: tasks/ (sub-agent
// results), tmp/ (inbound attachments) and the forums folder (its forums'
// results). Writes stay confined to the write area.
var AlwaysReadableWorkspaceDirs = []string{"tasks", "tmp", WorkspaceForumsDir}

// IsReservedWorkspaceName reports whether name, trimmed and in any case, is one
// of ReservedWorkspaceNames.
func IsReservedWorkspaceName(name string) bool {
	name = strings.TrimSpace(name)
	for _, r := range ReservedWorkspaceNames {
		if strings.EqualFold(name, r) {
			return true
		}
	}
	return false
}

// IgnoredMount is a configured mount set aside because its name is reserved.
type IgnoredMount struct {
	Agent string // agent id
	Mount string // the mount name as configured
}

// IgnoredMounts lists every configured mount whose name is reserved, in
// config order. EffectiveMounts leaves them out.
func (c *Config) IgnoredMounts() []IgnoredMount {
	var out []IgnoredMount
	for i := range c.Agents.List {
		ac := &c.Agents.List[i]
		for _, m := range ac.Mounts {
			if IsReservedWorkspaceName(m.Name) {
				out = append(out, IgnoredMount{Agent: ac.ID, Mount: m.Name})
			}
		}
	}
	return out
}

func (m IgnoredMount) key() string {
	return strings.ToLower(strings.TrimSpace(m.Agent)) + "|" + strings.ToLower(strings.TrimSpace(m.Mount))
}

// newReservedMounts returns, as errors, the mounts with a reserved name that
// next has and before does not. A mount already in the file is not refused,
// so it cannot block an unrelated save, including the one that renames it;
// it is ignored until then.
func newReservedMounts(before, next *Config) []error {
	return newProblems(before.IgnoredMounts(), next.IgnoredMounts(), IgnoredMount.key,
		func(m IgnoredMount) error {
			name := m.Agent
			if ac := next.AgentByID(m.Agent); ac != nil {
				name = ac.DisplayName()
			}
			return errors.New(name + "'s mount " + strconv.Quote(m.Mount) + " uses a reserved name; choose another name.")
		})
}

// warnReservedMounts logs one warning per mount set aside for a reserved name.
func warnReservedMounts(cfg *Config) {
	for _, m := range cfg.IgnoredMounts() {
		logger.WarnCF("config", "mount ignored: its name is reserved for a workspace folder; rename it",
			map[string]any{"agent": m.Agent, "mount": m.Mount})
	}
}
