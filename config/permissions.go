// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/logger"
)

// The areas of the data directory security.permissions sets modes for.
const (
	// AreaRoot is the data directory itself (dir_mode) and the regular files
	// directly in it, config.json among them (file_mode).
	AreaRoot = "root"
	// AreaSecrets is internal/, tls/ and tokens/ (dir_mode) and every secret
	// file wherever it lies (file_mode).
	AreaSecrets = "secrets"
	// AreaBin is <data dir>/bin, where operators keep scripts and binaries;
	// its files keep their execute bits.
	AreaBin = "bin"
	// AreaDefault is everything in the data directory no other area covers.
	AreaDefault = "default"
	AreaLogs    = "logs"
	AreaAgents  = "agents"
	AreaMedia   = "media"
	AreaCommon  = "common"
	AreaBackup  = "backup"
)

// BinDirName is the bin area's folder inside the data directory.
const BinDirName = "bin"

// The built-in modes: every folder 0700 and every file 0600, except the bin
// area's files, which are 0700 so scripts there stay executable.
const (
	DefaultDirMode     fs.FileMode = 0o700
	DefaultFileMode    fs.FileMode = 0o600
	DefaultBinFileMode fs.FileMode = 0o700
)

// PermissionsConfig is security.permissions: the exact modes of the folders
// and files under the data directory, one pair per area. Modes are octal
// strings ("0750"). Every key is written to config.json: a load fills a
// missing one with its built-in default (see completePermissions) and the
// gateway saves the result at start.
type PermissionsConfig struct {
	Root    AreaPermissions `json:"root,omitzero"`
	Secrets AreaPermissions `json:"secrets,omitzero"`
	Bin     AreaPermissions `json:"bin,omitzero"`
	Default AreaPermissions `json:"default,omitzero"`
	Logs    AreaPermissions `json:"logs,omitzero"`
	Agents  AreaPermissions `json:"agents,omitzero"`
	Media   AreaPermissions `json:"media,omitzero"`
	Common  AreaPermissions `json:"common,omitzero"`
	Backup  AreaPermissions `json:"backup,omitzero"`
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

// builtinModes is the built-in pair of area.
func builtinModes(area string) Modes {
	if area == AreaBin {
		return Modes{Dir: DefaultDirMode, File: DefaultBinFileMode}
	}
	return Modes{Dir: DefaultDirMode, File: DefaultFileMode}
}

// DefaultPermissions is the full block with every key at its built-in
// default, as a new install's config.json holds it.
func DefaultPermissions() PermissionsConfig {
	var p PermissionsConfig
	completePermissions(&p)
	return p
}

// namedArea is one area's name and a pointer to its configured pair.
type namedArea struct {
	name string
	perm *AreaPermissions
}

// permAreas lists p's areas in a fixed order, for validation and summaries.
func permAreas(p *PermissionsConfig) []namedArea {
	return []namedArea{
		{AreaRoot, &p.Root},
		{AreaSecrets, &p.Secrets},
		{AreaBin, &p.Bin},
		{AreaDefault, &p.Default},
		{AreaLogs, &p.Logs},
		{AreaAgents, &p.Agents},
		{AreaMedia, &p.Media},
		{AreaCommon, &p.Common},
		{AreaBackup, &p.Backup},
	}
}

// ownerFloor is the owner access every mode of area keeps: rwx on folders and
// rw on files, except bin, whose folder needs only r-x and files r--, so a
// read-only scripts folder is possible.
func ownerFloor(area string) Modes {
	if area == AreaBin {
		return Modes{Dir: 0o500, File: 0o400}
	}
	return Modes{Dir: 0o700, File: 0o600}
}

// Modes returns the effective modes of area; an unknown or empty area name
// gives the default area's. A key that is empty (a config built in memory,
// never loaded) or invalid (refused at load) gives the area's built-in mode,
// and a mode lacking the area's owner floor (ownerFloor) has those bits
// added: the file keeps what the operator wrote, and the load warns once
// (warnPermissionFloor).
func (p PermissionsConfig) Modes(area string) Modes {
	if area == "" {
		area = AreaDefault
	}
	for _, a := range permAreas(&p) {
		if a.name == area {
			def, floor := builtinModes(area), ownerFloor(area)
			return Modes{
				Dir:  modeOr(a.perm.DirMode, def.Dir) | floor.Dir,
				File: modeOr(a.perm.FileMode, def.File) | floor.File,
			}
		}
	}
	return p.Modes(AreaDefault)
}

// BelowFloor lists every configured mode lacking its area's owner floor, as
// "security.permissions.<area>.<key> 0600 lacks owner access; using 0700",
// in area order.
func (p PermissionsConfig) BelowFloor() []string {
	var out []string
	for _, a := range permAreas(&p) {
		floor := ownerFloor(a.name)
		for _, k := range []struct {
			key, value string
			floor      fs.FileMode
		}{{"dir_mode", a.perm.DirMode, floor.Dir}, {"file_mode", a.perm.FileMode, floor.File}} {
			if k.value == "" {
				continue
			}
			m, err := parseMode(k.value)
			if err != nil || m&k.floor == k.floor {
				continue
			}
			out = append(out, fmt.Sprintf("security.permissions.%s.%s %s lacks owner access; using %04o",
				a.name, k.key, k.value, m|k.floor))
		}
	}
	return out
}

// warnPermissionFloor logs one WARN per mode BelowFloor lists that old does
// not list (nil: every one).
func warnPermissionFloor(p PermissionsConfig, old *PermissionsConfig) {
	var seen []string
	if old != nil {
		seen = old.BelowFloor()
	}
	for _, msg := range p.BelowFloor() {
		if !slices.Contains(seen, msg) {
			logger.WarnCF("config", msg, nil)
		}
	}
}

// Summary lists every area's modes in a fixed order, e.g. "root 0700/0600;
// secrets 0700/0600; bin 0700/0700; ...".
func (p PermissionsConfig) Summary() string {
	parts := make([]string, 0, 9)
	for _, a := range permAreas(&p) {
		parts = append(parts, a.name+" "+p.Modes(a.name).String())
	}
	return strings.Join(parts, "; ")
}

// OtherAccess lists every configured mode that grants other users (the last
// octal digit) any access, as "security.permissions.<area>.<key> is 0644",
// in area order. Group access is not listed: granting it is an operator's
// ordinary choice.
func (p PermissionsConfig) OtherAccess() []string {
	var out []string
	for _, a := range permAreas(&p) {
		m := p.Modes(a.name)
		if m.Dir&0o007 != 0 {
			out = append(out, fmt.Sprintf("security.permissions.%s.dir_mode is %04o", a.name, m.Dir))
		}
		if m.File&0o007 != 0 {
			out = append(out, fmt.Sprintf("security.permissions.%s.file_mode is %04o", a.name, m.File))
		}
	}
	return out
}

// Validate refuses every mode that is not 3 or 4 octal digits, or whose
// fourth (leading) digit is not 0. Each refusal names its key. Missing owner
// bits are not a refusal: Modes adds the area's owner floor.
func (p PermissionsConfig) Validate() error {
	var errs []error
	check := func(key, value string) {
		if err := validateMode(value); err != nil {
			errs = append(errs, fmt.Errorf("security.permissions.%s is %q: %w", key, value, err))
		}
	}
	for _, a := range permAreas(&p) {
		check(a.name+".dir_mode", a.perm.DirMode)
		check(a.name+".file_mode", a.perm.FileMode)
	}
	return errors.Join(errs...)
}

// validateMode checks one mode string; empty is valid (filled at load).
func validateMode(value string) error {
	if value == "" {
		return nil
	}
	_, err := parseMode(value)
	return err
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

// formatMode renders a mode the way config.json holds it ("0700").
func formatMode(m fs.FileMode) string { return fmt.Sprintf("%04o", m.Perm()) }

// completePermissions fills every missing key with its built-in default and
// returns the keys it added, as "security.permissions.<area>.<key>" names.
func completePermissions(p *PermissionsConfig) (added []string) {
	for _, a := range permAreas(p) {
		def := builtinModes(a.name)
		if a.perm.DirMode == "" {
			a.perm.DirMode = formatMode(def.Dir)
			added = append(added, "security.permissions."+a.name+".dir_mode")
		}
		if a.perm.FileMode == "" {
			a.perm.FileMode = formatMode(def.File)
			added = append(added, "security.permissions."+a.name+".file_mode")
		}
	}
	return added
}

// permissionsFromJSON reads security.permissions from a config document and
// completes it, without decoding or resolving anything else. A document it
// cannot read gives the built-in defaults.
func permissionsFromJSON(data []byte) PermissionsConfig {
	var doc struct {
		Security struct {
			Permissions PermissionsConfig `json:"permissions"`
		} `json:"security"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return DefaultPermissions()
	}
	p := doc.Security.Permissions
	completePermissions(&p)
	return p
}

// ReadPermissions returns security.permissions from the config file at path,
// completed with the built-in defaults, for a command that needs the modes
// but must not load the whole config (which resolves secret references the
// command's environment may not have). A missing file gives the defaults.
func ReadPermissions(path string) (PermissionsConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the config path the caller resolved
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultPermissions(), nil
		}
		return PermissionsConfig{}, err
	}
	p := permissionsFromJSON(data)
	if err := p.Validate(); err != nil {
		return PermissionsConfig{}, err
	}
	return p, nil
}

// PermissionModes returns the modes for area (see PermissionsConfig.Modes).
func (c *Config) PermissionModes(area string) Modes {
	return c.Security.Permissions.Modes(area)
}

// PermissionsAdded returns the security.permissions keys the load filled
// with their defaults, and the keys the file's block holds that the config
// does not have (ignored): what the file differs by until the gateway writes
// the block (Store.SavePermissionDefaults).
func (c *Config) PermissionsAdded() (added, dropped []string) {
	return c.permsAdded, c.permsDropped
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
// workspace, the secrets area's otherwise (a temporary agent keeps its state
// under internal/ or the system temp directory, which stay private).
func (c *Config) AgentModes(path string) Modes {
	for _, dir := range c.AgentDirs() {
		if Within(dir, path) {
			return c.PermissionModes(AreaAgents)
		}
	}
	return c.PermissionModes(AreaSecrets)
}

// BackupDir is where backup archives are written: backup.dest, else
// <data dir>/backup.
func (c *Config) BackupDir() string {
	if d := strings.TrimSpace(c.Backup.Dest); d != "" {
		return d
	}
	return filepath.Join(c.dataDir, "backup")
}

// BinPath is the bin area's folder, <data dir>/bin.
func (c *Config) BinPath() string {
	return filepath.Join(c.dataDir, BinDirName)
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
