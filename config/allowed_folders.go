// ClawEh
// License: MIT

package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/PivotLLM/ClawEh/fileutil"
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
// writes only allowed_folders. Each migrated entry is logged once at WARN, and
// a mount that was read-only but whose folder the account can write gets a
// second WARN saying the agent can now write there. A
// mount whose path an allowed folder of the agent already has only merges its
// notify flag.
func (c *Config) migrateLegacyMounts() {
	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		for _, m := range a.LegacyMounts {
			path := strings.TrimSpace(m.Path)
			logger.WarnCF("config", "mounts is replaced by allowed_folders; migrated the entry (name and writable dropped, write access follows file permissions)",
				map[string]any{"agent": a.ID, "path": path})
			if !m.Writable && filepath.IsAbs(path) && fileutil.Writable(path) {
				logger.WarnCF("config", "a read-only mount is now writable by the agent: its folder's file permissions allow writes; make it read-only for the app's account to keep it read-only",
					map[string]any{"agent": a.ID, "path": path})
			}
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
	folders := a.CleanAllowedFolders()
	out := make([]string, 0, len(folders))
	for _, f := range folders {
		out = append(out, f.Path)
	}
	return out
}

// hasControlChar reports whether p holds a line break or another control
// character. Such a path is never usable: it would break the prompt's
// folder list and the new-file list kept for it.
func hasControlChar(p string) bool {
	return strings.IndexFunc(p, unicode.IsControl) >= 0
}

// CleanAllowedFolders returns the agent's allowed folders with clean absolute
// paths, in config order, each path once (the first entry for a path wins).
// A path that is not absolute (one a migration left) or holds a control
// character is skipped: it names no folder. Notes are trimmed.
func (a *AgentConfig) CleanAllowedFolders() []AllowedFolderConfig {
	if a == nil {
		return nil
	}
	var (
		out  []AllowedFolderConfig
		seen []string
	)
	for _, f := range a.AllowedFolders {
		p := strings.TrimSpace(f.Path)
		if !filepath.IsAbs(p) || hasControlChar(p) {
			continue
		}
		p = filepath.Clean(p)
		if slices.Contains(seen, p) {
			continue
		}
		seen = append(seen, p)
		out = append(out, AllowedFolderConfig{Path: p, Notify: f.Notify, Note: strings.TrimSpace(f.Note)})
	}
	return out
}

// MaxAllowedFolderNoteChars is the longest note an allowed folder may carry,
// in characters.
const MaxAllowedFolderNoteChars = 100

// longFolderNote is an allowed folder whose note is too long.
type longFolderNote struct {
	Agent string // agent id
	Path  string // the path as configured
	Note  string
}

func (p longFolderNote) key() string {
	return NormalizeAgentID(p.Agent) + "|" + p.Path + "|" + p.Note
}

// longFolderNotes lists every allowed folder whose note is longer than
// MaxAllowedFolderNoteChars characters, in config order.
func (c *Config) longFolderNotes() []longFolderNote {
	var out []longFolderNote
	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		for _, f := range a.AllowedFolders {
			if utf8.RuneCountInString(strings.TrimSpace(f.Note)) > MaxAllowedFolderNoteChars {
				out = append(out, longFolderNote{Agent: a.ID, Path: f.Path, Note: f.Note})
			}
		}
	}
	return out
}

// newLongFolderNotes returns, as errors, the allowed-folder notes longer than
// MaxAllowedFolderNoteChars that next has and before does not. One already in
// the file (a hand edit) is not refused, so it cannot block an unrelated save.
func newLongFolderNotes(before, next *Config) []error {
	return newProblems(before.longFolderNotes(), next.longFolderNotes(), longFolderNote.key,
		func(p longFolderNote) error {
			name := p.Agent
			if ac := next.AgentByID(p.Agent); ac != nil {
				name = ac.DisplayName()
			}
			return errors.New(name + "'s note for " + strconv.Quote(p.Path) + " is longer than " +
				strconv.Itoa(MaxAllowedFolderNoteChars) + " characters.")
		})
}

// allowedFolderProblem is an allowed folder whose path is not absolute or
// holds a control character.
type allowedFolderProblem struct {
	Agent   string // agent id
	Path    string // the path as configured
	Control bool   // the path holds a control character
}

func (p allowedFolderProblem) key() string {
	return NormalizeAgentID(p.Agent) + "|" + p.Path
}

// allowedFolderProblems lists every allowed folder whose path is not an
// absolute path or holds a control character, in config order.
func (c *Config) allowedFolderProblems() []allowedFolderProblem {
	var out []allowedFolderProblem
	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		for _, f := range a.AllowedFolders {
			p := strings.TrimSpace(f.Path)
			switch {
			case hasControlChar(p):
				out = append(out, allowedFolderProblem{Agent: a.ID, Path: f.Path, Control: true})
			case !filepath.IsAbs(p):
				out = append(out, allowedFolderProblem{Agent: a.ID, Path: f.Path})
			}
		}
	}
	return out
}

// newAllowedFolderProblems returns, as errors, the allowed folders with a
// path that is not absolute, or that holds a control character, that next
// has and before does not. One already in the file (a migrated relative
// mount path, a hand edit) is not refused, so it cannot block an unrelated
// save; the file tools skip it and it is listed as ignored.
func newAllowedFolderProblems(before, next *Config) []error {
	return newProblems(before.allowedFolderProblems(), next.allowedFolderProblems(), allowedFolderProblem.key,
		func(p allowedFolderProblem) error {
			name := p.Agent
			if ac := next.AgentByID(p.Agent); ac != nil {
				name = ac.DisplayName()
			}
			if p.Control {
				return errors.New(name + "'s allowed folder " + strconv.Quote(p.Path) + " contains a line break or other control character.")
			}
			return errors.New(name + "'s allowed folder " + strconv.Quote(p.Path) + " must be an absolute path.")
		})
}

// Reasons an allowed folder is ignored (IgnoredAllowedFolder.Reason).
const (
	FolderNotAbsolute = "not_absolute"
	FolderNotFound    = "not_found"
	FolderControlChar = "control_char"
)

// IgnoredAllowedFolder is a configured allowed folder the file tools skip.
type IgnoredAllowedFolder struct {
	Agent  string // agent id
	Path   string // the path as configured
	Reason string // FolderNotAbsolute, FolderNotFound or FolderControlChar
}

// IgnoredAllowedFolders lists every configured allowed folder the file tools
// skip, in config order: a path with a line break or other control
// character, one that is not absolute (one a migration left), or one that is
// not an existing directory.
func (c *Config) IgnoredAllowedFolders() []IgnoredAllowedFolder {
	var out []IgnoredAllowedFolder
	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		for _, f := range a.AllowedFolders {
			p := strings.TrimSpace(f.Path)
			switch {
			case hasControlChar(p):
				out = append(out, IgnoredAllowedFolder{Agent: a.ID, Path: f.Path, Reason: FolderControlChar})
			case !filepath.IsAbs(p):
				out = append(out, IgnoredAllowedFolder{Agent: a.ID, Path: f.Path, Reason: FolderNotAbsolute})
			case !isDir(p):
				out = append(out, IgnoredAllowedFolder{Agent: a.ID, Path: f.Path, Reason: FolderNotFound})
			}
		}
	}
	return out
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
