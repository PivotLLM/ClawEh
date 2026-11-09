// ClawEh
// License: MIT

package config

import (
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/logger"
)

// LegacyMountConfig is one entry of the retired agents.list[].mounts list.
// Only its path and notify flag survive the migration to allowed folders.
type LegacyMountConfig struct {
	Name     string `json:"name,omitempty"`
	Path     string `json:"path,omitempty"`
	Notify   bool   `json:"notify,omitempty"`
	Writable bool   `json:"writable,omitempty"`
}

// migrateLegacyMounts turns each agent's retired mounts into allowed folders
// (path and notify kept; the name and the writable flag are dropped, since
// write access now follows file permissions) and clears them, so the next save
// writes only allowed_folders. Each migrated entry is logged once at WARN. A
// mount whose path an allowed folder of the agent already has only merges its
// notify flag.
func (c *Config) migrateLegacyMounts() {
	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		for _, m := range a.LegacyMounts {
			path := strings.TrimSpace(m.Path)
			logger.WarnCF("config", "mounts is replaced by allowed_folders; migrated the entry (name and writable dropped, write access follows file permissions)",
				map[string]any{"agent": a.ID, "path": path})
			merged := false
			for j := range a.AllowedFolders {
				if a.AllowedFolders[j].Path == path {
					a.AllowedFolders[j].Notify = a.AllowedFolders[j].Notify || m.Notify
					merged = true
					break
				}
			}
			if !merged {
				a.AllowedFolders = append(a.AllowedFolders, AllowedFolderConfig{Path: path, Notify: m.Notify})
			}
		}
		a.LegacyMounts = nil
	}
}

// AllowedFolderPaths returns the agent's allowed folders as clean absolute
// paths, in config order, each once. A path that is not absolute (one a
// migration left) is skipped: it names no folder.
func (a *AgentConfig) AllowedFolderPaths() []string {
	if a == nil {
		return nil
	}
	var out []string
	for _, f := range a.AllowedFolders {
		p := strings.TrimSpace(f.Path)
		if !filepath.IsAbs(p) {
			continue
		}
		p = filepath.Clean(p)
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// allowedFolderProblem is an allowed folder whose path is not absolute.
type allowedFolderProblem struct {
	Agent string // agent id
	Path  string // the path as configured
}

func (p allowedFolderProblem) key() string {
	return NormalizeAgentID(p.Agent) + "|" + p.Path
}

// allowedFolderProblems lists every allowed folder whose path is not an
// absolute path, in config order.
func (c *Config) allowedFolderProblems() []allowedFolderProblem {
	var out []allowedFolderProblem
	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		for _, f := range a.AllowedFolders {
			if !filepath.IsAbs(strings.TrimSpace(f.Path)) {
				out = append(out, allowedFolderProblem{Agent: a.ID, Path: f.Path})
			}
		}
	}
	return out
}

// newAllowedFolderProblems returns, as errors, the allowed folders with a
// path that is not absolute that next has and before does not. One already in
// the file (a migrated relative mount path) is not refused, so it cannot block
// an unrelated save; the file tools skip it with a warning.
func newAllowedFolderProblems(before, next *Config) []error {
	return newProblems(before.allowedFolderProblems(), next.allowedFolderProblems(), allowedFolderProblem.key,
		func(p allowedFolderProblem) error {
			name := p.Agent
			if ac := next.AgentByID(p.Agent); ac != nil {
				name = ac.DisplayName()
			}
			return errors.New(name + "'s allowed folder " + strconv.Quote(p.Path) + " must be an absolute path.")
		})
}
