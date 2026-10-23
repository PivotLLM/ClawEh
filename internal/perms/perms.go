/******************************************************************************
 * Copyright (c) 2026 Tenebris Technologies Inc.                              *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

// Package perms enforces the file permissions of the data directory at
// startup. CLAW_HOME holds the config (with provider keys), OAuth token
// stores, device pairing tokens, TLS keys and session archives. Secrets are
// never meant for other users of the host; the rest follows the modes in
// security.permissions (owner-only by default). Enforce tightens what it can
// and refuses to continue when the config itself is readable by others;
// Check is the report-only walk the configuration report uses.
package perms

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
)

// Finding is one path whose permissions grant group or other access its
// governing mode does not.
type Finding struct {
	Path string
	Mode fs.FileMode // current permission bits
	Want fs.FileMode // the same bits with the ungranted group/other bits cleared
}

// Policy is what Enforce and Check hold the data directory to. Group and
// other bits a governing mode does not grant are cleared; nothing is ever
// loosened, and owner bits are never changed.
type Policy struct {
	// DirMode governs the data directory itself and every directory outside
	// an area. Zero means 0700.
	DirMode fs.FileMode
	// Areas govern their subtrees; the deepest root containing a path wins.
	Areas []Area
}

// Area is one subtree with its own modes.
type Area struct {
	Root    string
	DirMode fs.FileMode
	// FileMode governs the SQLite databases (and their side files) under
	// Root when Databases is set; every other secret stays 0600.
	FileMode  fs.FileMode
	Databases bool
}

// privateDirs are the data directory's folders that hold only secrets: they
// stay owner-only whatever the configured modes.
var privateDirs = []string{global.InternalDir, "tls", "tokens"}

// PolicyFor is the policy security.permissions in cfg describes: the default
// directory mode for the data directory, the logs, media, common and backup
// areas, the agents area (agents.base_dir and every configured workspace,
// whose databases follow the area's file mode), and the secrets-only folders
// at 0700.
func PolicyFor(cfg *config.Config) Policy {
	p := Policy{DirMode: cfg.PermissionModes("").Dir}
	add := func(root, area string, databases bool) {
		m := cfg.PermissionModes(area)
		p.Areas = append(p.Areas, Area{Root: root, DirMode: m.Dir, FileMode: m.File, Databases: databases})
	}
	dd := cfg.DataDir()
	add(filepath.Join(dd, "logs"), config.AreaLogs, false)
	add(filepath.Join(dd, "media"), config.AreaMedia, false)
	add(cfg.ResolveCommonDir(), config.AreaCommon, false)
	add(cfg.BackupDir(), config.AreaBackup, false)
	for _, dir := range cfg.AgentDirs() {
		add(dir, config.AreaAgents, true)
	}
	for _, name := range privateDirs {
		p.Areas = append(p.Areas, Area{Root: filepath.Join(dd, name), DirMode: config.DefaultDirMode, FileMode: config.DefaultFileMode})
	}
	return p
}

// dirMode is the policy's default directory mode.
func (p Policy) dirMode() fs.FileMode {
	if p.DirMode == 0 {
		return config.DefaultDirMode
	}
	return p.DirMode
}

// area returns the area governing path, the one with the deepest root
// containing it, or nil.
func (p Policy) area(path string) *Area {
	var best *Area
	for i := range p.Areas {
		a := &p.Areas[i]
		if a.Root == "" || !config.Within(a.Root, path) {
			continue
		}
		if best == nil || len(filepath.Clean(a.Root)) > len(filepath.Clean(best.Root)) {
			best = a
		}
	}
	return best
}

// ErrTruncated is returned by Check when the walk hit the entry cap before
// reaching the end of the tree; the findings collected up to then are still
// returned.
var ErrTruncated = errors.New("perms: walk truncated at the entry cap")

// walkLimit bounds the number of directory entries one walk visits. A data
// directory with more is not a normal install and the walk stops rather than
// delaying startup; a variable so tests can lower it.
var walkLimit = 50000

// groupOther is the permission mask for everyone who is not the owner.
const groupOther fs.FileMode = 0o077

// want returns mode with the group/other bits allowed does not grant cleared.
func want(mode, allowed fs.FileMode) fs.FileMode {
	return mode &^ (groupOther &^ allowed)
}

// skipDirs are subtrees the walk never enters: media is large and holds no
// secrets, logs hold none either and are written continuously.
var skipDirs = map[string]bool{"media": true, "logs": true}

// Enforce holds the data directory to policy. It creates dataDir with the
// policy's directory mode if it is missing and tightens it if it is looser;
// refuses (returns an error the caller should treat as fatal) when configPath
// grants any group or other access, naming the chmod that fixes it; and
// clears the group/other bits the governing mode does not grant on every
// directory and every secret-bearing file under dataDir (see isSensitive).
// Symlinks are never followed or changed, and the media/ and logs/ trees are
// not entered. Every change and every failure to change is reported through
// log, which may be nil. On Windows it does nothing.
func Enforce(dataDir, configPath string, policy Policy, log func(msg string, fields map[string]any)) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if log == nil {
		log = func(string, map[string]any) {}
	}
	if err := enforceDataDir(dataDir, policy.dirMode(), log); err != nil {
		return err
	}
	if err := CheckConfig(configPath); err != nil {
		return err
	}
	truncated, err := walk(dataDir, policy, func(f Finding) {
		if err := os.Chmod(f.Path, f.Want); err != nil {
			log("could not tighten file permissions", map[string]any{
				"path": f.Path, "mode": fmt.Sprintf("%04o", f.Mode), "error": err.Error(),
			})
			return
		}
		log("tightened file permissions", map[string]any{
			"path": f.Path, "from": fmt.Sprintf("%04o", f.Mode), "to": fmt.Sprintf("%04o", f.Want),
		})
	})
	if err != nil {
		return err
	}
	if truncated {
		log("permission walk stopped at the entry cap; files beyond it were not checked",
			map[string]any{"dir": dataDir, "limit": walkLimit})
	}
	return nil
}

// Check is the report-only counterpart of Enforce: it walks the same paths
// with the same rules and returns every offender without changing anything.
// dataDir itself and configPath are included when they are loose. When the
// walk hits the entry cap the findings so far are returned with ErrTruncated.
// On Windows it returns nothing.
func Check(dataDir, configPath string, policy Policy) ([]Finding, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	var findings []Finding
	if f, ok, err := loose(dataDir, policy.dirMode()); err != nil && !os.IsNotExist(err) {
		return nil, err
	} else if ok {
		findings = append(findings, f)
	}
	if f, ok, err := loose(configPath, 0); err != nil && !os.IsNotExist(err) {
		return nil, err
	} else if ok {
		findings = append(findings, f)
	}
	truncated, err := walk(dataDir, policy, func(f Finding) { findings = append(findings, f) })
	if err != nil {
		return nil, err
	}
	if truncated {
		return findings, ErrTruncated
	}
	return findings, nil
}

// EnsurePrivateFile creates path as an empty 0600 file when it does not exist
// and clears group/other bits on it when it does, so that whatever opens it
// next finds it private. Store openers call it before sql.Open: SQLite creates
// a new database with the umask default (0644) and gives the -wal and -shm
// side files the main file's mode, so a private main file keeps them private
// too, from the first write rather than from the next restart. An empty file
// is a valid empty SQLite database. On Windows it does nothing.
func EnsurePrivateFile(path string) error {
	return EnsureFileMode(path, 0o600)
}

// EnsureFileMode creates path as an empty file with mode (narrowed by the
// umask) when it does not exist, and clears the group/other bits mode does
// not grant when it does. It never loosens a file. A symbolic link is
// refused. On Windows it does nothing.
func EnsureFileMode(path string, mode fs.FileMode) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	// A symbolic link is refused, never followed: tightening or creating
	// through it would act on a file somewhere else.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("perms: %s is a symbolic link", path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY|oNoFollow, mode.Perm()) //nolint:gosec // the store path the caller is about to open
	if err != nil {
		return fmt.Errorf("perms: create %s: %w", path, err)
	}
	err = tightenOpen(f, mode.Perm())
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("perms: %s: %w", path, err)
	}
	return nil
}

// tightenOpen clears the group/other bits allowed does not grant on the open
// regular file f, acting on the descriptor so no path is resolved again.
func tightenOpen(f *os.File, allowed fs.FileMode) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if mode := fi.Mode().Perm(); want(mode, allowed) != mode {
		return f.Chmod(want(mode, allowed))
	}
	return nil
}

// enforceDataDir creates dataDir with dirMode when missing and tightens it
// when it grants group/other access dirMode does not. A failed chmod (a
// directory owned by someone else) is logged, not fatal: the operator can
// still run, and the config check that follows is what guards the secrets.
func enforceDataDir(dataDir string, dirMode fs.FileMode, log func(string, map[string]any)) error {
	f, ok, err := loose(dataDir, dirMode)
	if os.IsNotExist(err) {
		if mkErr := os.MkdirAll(dataDir, dirMode); mkErr != nil {
			return fmt.Errorf("perms: create %s: %w", dataDir, mkErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("perms: stat %s: %w", dataDir, err)
	}
	if !ok {
		return nil
	}
	if err := os.Chmod(dataDir, f.Want); err != nil {
		log("could not tighten data directory permissions", map[string]any{
			"path": dataDir, "mode": fmt.Sprintf("%04o", f.Mode), "error": err.Error(),
		})
		return nil //nolint:nilerr // logged and non-fatal by design; see the doc comment
	}
	log("tightened data directory permissions", map[string]any{
		"path": dataDir, "from": fmt.Sprintf("%04o", f.Mode), "to": fmt.Sprintf("%04o", f.Want),
	})
	return nil
}

// CheckConfig refuses a config file that anyone but the owner can read or
// write. It is never chmod'ed on the operator's behalf: a loose config is a
// sign that something else on the host has been at it, and the operator
// should see that rather than have it papered over. A missing config is fine
// (first run seeds one). It needs nothing from the config, so it runs before
// the config is read. On Windows it does nothing.
func CheckConfig(configPath string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, ok, err := loose(configPath, 0)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("perms: stat %s: %w", configPath, err)
	}
	if !ok {
		return nil
	}
	return fmt.Errorf("perms: %s is readable by other users (mode %04o); it holds credentials, so refusing to start. Fix with: chmod 600 %s",
		configPath, f.Mode, configPath)
}

// loose stats path (following a symlink, since it is the target's bits that
// matter) and reports whether group or other has access allowed does not
// grant.
func loose(path string, allowed fs.FileMode) (Finding, bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Finding{}, false, err
	}
	mode := fi.Mode().Perm()
	if w := want(mode, allowed); w != mode {
		return Finding{Path: path, Mode: mode, Want: w}, true, nil
	}
	return Finding{}, false, nil
}

// walk visits every directory and regular file under dataDir, calling found
// for each directory, and each sensitive file, whose mode grants group/other
// access its governing mode in policy does not. It reports whether it
// stopped at walkLimit. Unreadable subtrees are skipped, not fatal.
func walk(dataDir string, policy Policy, found func(Finding)) (bool, error) {
	dataDir = filepath.Clean(dataDir)
	seen := 0
	truncated := false
	err := filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dataDir {
				return err
			}
			// An unreadable entry below the root is skipped, not fatal: one
			// odd subtree must not stop the rest of the walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == dataDir {
			return nil
		}
		seen++
		if seen > walkLimit {
			truncated = true
			return fs.SkipAll
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			// Every directory ClawEh creates under the data directory has its
			// area's mode (owner-only by default), so a looser one (made
			// before that rule, or by a library with its own default) is
			// tightened like a secret file.
			allowed := policy.dirMode()
			if a := policy.area(path); a != nil {
				allowed = a.DirMode
			}
			if info, infoErr := d.Info(); infoErr == nil {
				if mode := info.Mode().Perm(); want(mode, allowed) != mode {
					found(Finding{Path: path, Mode: mode, Want: want(mode, allowed)})
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		// WalkDir builds every path as Join(root, ...), so the relative path
		// is what follows the cleaned root and its separator.
		rel := strings.TrimPrefix(path, dataDir+string(filepath.Separator))
		if !isSensitive(rel) {
			return nil
		}
		// Secrets are owner-only; the databases of an area that opts in (the
		// agents' session archives and memories) follow its file mode.
		allowed := fs.FileMode(0o600)
		if a := policy.area(path); a != nil && a.Databases && isDatabase(rel) && !secretName(rel) {
			allowed = a.FileMode
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil //nolint:nilerr // the entry vanished mid-walk (a WAL side file, say); nothing to tighten
		}
		mode := info.Mode().Perm()
		if w := want(mode, allowed); w != mode {
			found(Finding{Path: path, Mode: mode, Want: w})
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("perms: walk %s: %w", dataDir, err)
	}
	return truncated, nil
}

// isSensitive decides by name and location, relative to the data directory,
// whether a file holds secrets: SQLite databases and their WAL/shm side files
// (*.db, *.db-wal, *.db-shm, *.sqlite*), credentials.json, internal/*.json,
// anything under tokens/, tls/*.key, and any file whose name mentions a token
// or secret.
func isSensitive(rel string) bool {
	rel = filepath.ToSlash(rel)
	name := strings.ToLower(filepath.Base(rel))
	ext := filepath.Ext(name)
	if isDatabase(rel) || secretName(rel) || name == "credentials.json" {
		return true
	}
	top, rest, _ := strings.Cut(rel, "/")
	switch top {
	case "tokens":
		return rest != ""
	case global.InternalDir:
		return !strings.Contains(rest, "/") && ext == ".json"
	case "tls":
		return ext == ".key"
	}
	return false
}

// isDatabase reports by name whether the file is a SQLite database or one of
// its WAL/shm side files.
func isDatabase(rel string) bool {
	name := strings.ToLower(filepath.Base(rel))
	ext := filepath.Ext(name)
	return ext == ".db" || ext == ".db-wal" || ext == ".db-shm" || strings.Contains(name, ".sqlite")
}

// secretName reports whether the file's name mentions a token or secret.
func secretName(rel string) bool {
	name := strings.ToLower(filepath.Base(rel))
	return strings.Contains(name, "token") || strings.Contains(name, "secret")
}
