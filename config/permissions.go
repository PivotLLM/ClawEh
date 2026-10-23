// ClawEh
// License: MIT

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The areas of the data directory whose modes security.permissions can set
// apart from the default.
const (
	AreaLogs   = "logs"
	AreaAgents = "agents"
	AreaMedia  = "media"
	AreaCommon = "common"
	AreaBackup = "backup"
)

// The modes ClawEh creates directories and files with when
// security.permissions sets none.
const (
	DefaultDirMode  fs.FileMode = 0o700
	DefaultFileMode fs.FileMode = 0o600
)

// PermissionsConfig is security.permissions: the modes of the directories and
// files ClawEh creates under the data directory. DirMode and FileMode are the
// default for every area; an area's own pair overrides them. Modes are octal
// strings ("0750"); an empty one keeps the default. Secrets (config.json,
// credentials.json, TLS keys, internal/, token stores) stay 0600 whatever is
// set here.
type PermissionsConfig struct {
	DirMode  string          `json:"dir_mode,omitempty"`
	FileMode string          `json:"file_mode,omitempty"`
	Logs     AreaPermissions `json:"logs,omitzero"`
	Agents   AreaPermissions `json:"agents,omitzero"`
	Media    AreaPermissions `json:"media,omitzero"`
	Common   AreaPermissions `json:"common,omitzero"`
	Backup   AreaPermissions `json:"backup,omitzero"`
}

// AreaPermissions is one area's directory and file mode, as octal strings.
type AreaPermissions struct {
	DirMode  string `json:"dir_mode,omitempty"`
	FileMode string `json:"file_mode,omitempty"`
}

// Modes is a resolved directory and file mode pair.
type Modes struct {
	Dir  fs.FileMode
	File fs.FileMode
}

// String renders the pair as "0750/0640".
func (m Modes) String() string { return fmt.Sprintf("%04o/%04o", m.Dir, m.File) }

// namedArea is one area's name and its configured pair.
type namedArea struct {
	name string
	perm AreaPermissions
}

// areas lists the areas in a fixed order, for validation and summaries.
func (p PermissionsConfig) areas() []namedArea {
	return []namedArea{
		{AreaLogs, p.Logs},
		{AreaAgents, p.Agents},
		{AreaMedia, p.Media},
		{AreaCommon, p.Common},
		{AreaBackup, p.Backup},
	}
}

// Modes returns the modes for area; an unknown or empty area gives the
// default pair, which also governs the data directory itself. An invalid
// value (refused at load) counts as unset.
func (p PermissionsConfig) Modes(area string) Modes {
	m := Modes{Dir: modeOr(p.DirMode, DefaultDirMode), File: modeOr(p.FileMode, DefaultFileMode)}
	for _, a := range p.areas() {
		if a.name == area {
			m.Dir = modeOr(a.perm.DirMode, m.Dir)
			m.File = modeOr(a.perm.FileMode, m.File)
		}
	}
	return m
}

// Summary describes the effective modes in a few words: the default
// directory mode and the secrets' fixed 0600, then each area whose modes
// differ from the default pair, e.g. "folders 0700, secrets 0600; logs
// 0750/0640".
func (p PermissionsConfig) Summary() string {
	def := p.Modes("")
	parts := []string{fmt.Sprintf("folders %04o, secrets 0600", def.Dir)}
	for _, a := range p.areas() {
		if m := p.Modes(a.name); m != def {
			parts = append(parts, a.name+" "+m.String())
		}
	}
	return strings.Join(parts, "; ")
}

// Validate refuses every mode that is not 3 or 4 octal digits, a directory
// mode without owner read/write/execute, a file mode without owner
// read/write, and any mode giving other users write access. Each refusal
// names its key.
func (p PermissionsConfig) Validate() error {
	var errs []error
	check := func(key, value string, dir bool) {
		if err := validateMode(value, dir); err != nil {
			errs = append(errs, fmt.Errorf("security.permissions.%s is %q: %w", key, value, err))
		}
	}
	check("dir_mode", p.DirMode, true)
	check("file_mode", p.FileMode, false)
	for _, a := range p.areas() {
		check(a.name+".dir_mode", a.perm.DirMode, true)
		check(a.name+".file_mode", a.perm.FileMode, false)
	}
	return errors.Join(errs...)
}

// validateMode checks one mode string; empty is valid (the default).
func validateMode(value string, dir bool) error {
	if value == "" {
		return nil
	}
	m, err := parseMode(value)
	if err != nil {
		return err
	}
	switch {
	case dir && m&0o700 != 0o700:
		return errors.New("folders need owner read, write and execute (7)")
	case !dir && m&0o600 != 0o600:
		return errors.New("files need owner read and write (6)")
	case m&0o002 != 0:
		return errors.New("other users may not have write access")
	}
	return nil
}

// parseMode parses 3 or 4 octal digits; a fourth (leading) digit must be 0,
// since only permission bits are set.
func parseMode(value string) (fs.FileMode, error) {
	bad := errors.New(`use 3 or 4 octal digits, such as "0750"`)
	if n := len(value); n != 3 && n != 4 {
		return 0, bad
	}
	for _, c := range value {
		if c < '0' || c > '7' {
			return 0, bad
		}
	}
	if len(value) == 4 && value[0] != '0' {
		return 0, errors.New("setuid, setgid and sticky bits are not supported")
	}
	v, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0, bad
	}
	return fs.FileMode(v), nil
}

// modeOr parses value, or returns def when it is empty or invalid.
func modeOr(value string, def fs.FileMode) fs.FileMode {
	if value == "" {
		return def
	}
	m, err := parseMode(value)
	if err != nil {
		return def
	}
	return m
}

// PermissionModes returns the modes for area (see PermissionsConfig.Modes).
func (c *Config) PermissionModes(area string) Modes {
	return c.Security.Permissions.Modes(area)
}

// AgentDirs returns the directories of the agents area: agents.base_dir and
// every configured agent's own workspace, cleaned and de-duplicated.
func (c *Config) AgentDirs() []string {
	dirs := []string{filepath.Clean(c.BaseDir())}
	for _, ac := range c.Agents.List {
		ws := strings.TrimSpace(ac.Workspace)
		if ws == "" {
			continue
		}
		ws = filepath.Clean(ExpandHome(ws))
		if !slices.Contains(dirs, ws) {
			dirs = append(dirs, ws)
		}
	}
	return dirs
}

// AgentModes returns the modes for something kept at path for an agent: the
// agents area's when path lies in agents.base_dir or a configured agent's
// workspace, the defaults otherwise (a temporary agent keeps its state under
// internal/ or the system temp directory, which stay private).
func (c *Config) AgentModes(path string) Modes {
	for _, dir := range c.AgentDirs() {
		if Within(dir, path) {
			return c.PermissionModes(AreaAgents)
		}
	}
	return Modes{Dir: DefaultDirMode, File: DefaultFileMode}
}

// BackupDir is where backup archives are written: backup.dest, else
// <data dir>/backup.
func (c *Config) BackupDir() string {
	if d := strings.TrimSpace(c.Backup.Dest); d != "" {
		return d
	}
	return filepath.Join(c.dataDir, "backup")
}

// Within reports whether path is dir or lies under it, comparing absolute
// cleaned paths lexically (symbolic links are not resolved).
func Within(dir, path string) bool {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
