/******************************************************************************
 * Copyright (c) 2026 Tenebris Technologies Inc.                              *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

// Package perms enforces the file permissions of the data directory at
// startup. CLAW_HOME holds the config (with provider keys), OAuth token
// stores, device pairing tokens, TLS keys and session archives, and none of
// it is meant for other users of the host. Enforce tightens what it can and
// refuses to continue when the config itself is readable by others; Check is
// the report-only walk the configuration report uses.
package perms

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Finding is one path whose permissions grant group or other access.
type Finding struct {
	Path string
	Mode fs.FileMode // current permission bits
	Want fs.FileMode // the same bits with group/other cleared
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

// skipDirs are subtrees the walk never enters: media is large and holds no
// secrets, logs hold none either and are written continuously.
var skipDirs = map[string]bool{"media": true, "logs": true}

// Enforce makes the data directory private. It creates dataDir 0700 if it is
// missing and tightens it to 0700 if it is looser; refuses (returns an error
// the caller should treat as fatal) when configPath grants any group or other
// access, naming the chmod that fixes it; and clears group/other bits on every
// secret-bearing file under dataDir (see isSensitive). Symlinks are never
// followed or changed, and the media/ and logs/ trees are not entered. Every
// change and every failure to change is reported through log, which may be
// nil. On Windows it does nothing.
func Enforce(dataDir, configPath string, log func(msg string, fields map[string]any)) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if log == nil {
		log = func(string, map[string]any) {}
	}
	if err := enforceDataDir(dataDir, log); err != nil {
		return err
	}
	if err := checkConfig(configPath); err != nil {
		return err
	}
	truncated, err := walk(dataDir, func(f Finding) {
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
func Check(dataDir, configPath string) ([]Finding, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	var findings []Finding
	if f, ok, err := loose(dataDir); err != nil && !os.IsNotExist(err) {
		return nil, err
	} else if ok {
		findings = append(findings, f)
	}
	if f, ok, err := loose(configPath); err != nil && !os.IsNotExist(err) {
		return nil, err
	} else if ok {
		findings = append(findings, f)
	}
	truncated, err := walk(dataDir, func(f Finding) { findings = append(findings, f) })
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
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600) //nolint:gosec // the store path the caller is about to open
	if err != nil {
		return fmt.Errorf("perms: create %s: %w", path, err)
	}
	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("perms: close %s: %w", path, closeErr)
	}
	fnd, ok, err := loose(path)
	if err != nil {
		return fmt.Errorf("perms: stat %s: %w", path, err)
	}
	if !ok {
		return nil
	}
	if chmodErr := os.Chmod(path, fnd.Want); chmodErr != nil {
		return fmt.Errorf("perms: tighten %s: %w", path, chmodErr)
	}
	return nil
}

// enforceDataDir creates dataDir 0700 when missing and tightens it when it
// grants group/other access. A failed chmod (a directory owned by someone
// else) is logged, not fatal: the operator can still run, and the config
// check that follows is what guards the secrets.
func enforceDataDir(dataDir string, log func(string, map[string]any)) error {
	f, ok, err := loose(dataDir)
	if os.IsNotExist(err) {
		if mkErr := os.MkdirAll(dataDir, 0o700); mkErr != nil {
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

// checkConfig refuses a config file that anyone but the owner can read or
// write. It is never chmod'ed on the operator's behalf: a loose config is a
// sign that something else on the host has been at it, and the operator
// should see that rather than have it papered over. A missing config is fine
// (first run seeds one).
func checkConfig(configPath string) error {
	f, ok, err := loose(configPath)
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
// matter) and reports whether group or other has any access to it.
func loose(path string) (Finding, bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Finding{}, false, err
	}
	mode := fi.Mode().Perm()
	if mode&groupOther == 0 {
		return Finding{}, false, nil
	}
	return Finding{Path: path, Mode: mode, Want: mode &^ groupOther}, true, nil
}

// walk visits every regular file under dataDir, calling found for each
// sensitive one whose mode grants group/other access. It reports whether it
// stopped at walkLimit. Unreadable subtrees are skipped, not fatal.
func walk(dataDir string, found func(Finding)) (bool, error) {
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
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		// WalkDir builds every path as Join(root, ...), so the relative path
		// is what follows the cleaned root and its separator.
		if !isSensitive(strings.TrimPrefix(path, dataDir+string(filepath.Separator))) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil //nolint:nilerr // the entry vanished mid-walk (a WAL side file, say); nothing to tighten
		}
		mode := info.Mode().Perm()
		if mode&groupOther == 0 {
			return nil
		}
		found(Finding{Path: path, Mode: mode, Want: mode &^ groupOther})
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
// (*.db, *.db-wal, *.db-shm, *.sqlite*), credentials.json, state/*.json,
// anything under tokens/, tls/*.key, and any file whose name mentions a token
// or secret.
func isSensitive(rel string) bool {
	rel = filepath.ToSlash(rel)
	name := strings.ToLower(filepath.Base(rel))
	ext := filepath.Ext(name)
	switch {
	case ext == ".db" || ext == ".db-wal" || ext == ".db-shm":
		return true
	case strings.Contains(name, ".sqlite"):
		return true
	case name == "credentials.json":
		return true
	case strings.Contains(name, "token") || strings.Contains(name, "secret"):
		return true
	}
	top, rest, _ := strings.Cut(rel, "/")
	switch top {
	case "tokens":
		return rest != ""
	case "state":
		return !strings.Contains(rest, "/") && ext == ".json"
	case "tls":
		return ext == ".key"
	}
	return false
}
