/******************************************************************************
 * Copyright (c) 2026 Tenebris Technologies Inc.                              *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

// Package perms holds the data directory to the modes security.permissions
// sets. CLAW_HOME holds the config (with provider keys), OAuth token stores,
// device pairing tokens, TLS keys and session archives. Every folder and
// every regular file in it belongs to one area (root, secrets, bin, logs,
// agents, media, common, backup, default) and Enforce sets each to exactly
// its area's mode at startup, widening as well as tightening. CheckConfig
// refuses a config file more open than the root area allows; Check is the
// report-only walk the configuration report uses.
package perms

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/global"
)

// Finding is one path whose mode differs from the one its area sets.
type Finding struct {
	Path string
	Mode fs.FileMode // current permission bits
	Want fs.FileMode // the configured mode

	// real is the path to change (the data directory resolved through a
	// symbolic link) and info what the walk found there; setMode changes the
	// entry only while it is still that one.
	real string
	info fs.FileInfo
}

// Looser reports whether the entry grants a bit its configured mode does not.
func (f Finding) Looser() bool { return f.Mode&^f.Want != 0 }

// Policy is what Enforce and Check hold the data directory to.
type Policy struct {
	// DataDir is the data directory the policy describes (its configured
	// spelling).
	DataDir string
	// Root governs the data directory itself (Dir) and the regular files
	// directly in it that no area claims (File).
	Root config.Modes
	// Secrets governs every secret file wherever it lies (see isSecretFile).
	Secrets config.Modes
	// Default governs every folder and file no area claims.
	Default config.Modes
	// Areas govern their subtrees; the deepest root containing a path wins.
	Areas []Area
}

// Area is one subtree with its own modes.
type Area struct {
	Root  string
	Modes config.Modes
	// Databases gives the area's SQLite databases (and their side files) the
	// area's file mode instead of the secrets' (the agents' session archives
	// and memories).
	Databases bool
	// Secret makes every file in the area a secret (internal/, tls/,
	// tokens/).
	Secret bool
	// Bin gives every file in the area its file mode, even one named like a
	// secret, so a script there keeps its execute bits.
	Bin bool
	// Plain gives every file in the area its file mode whatever its name: a
	// log or a downloaded attachment named like a token or a database is
	// still a log or an attachment (logs/, media/).
	Plain bool
}

// Logger receives what Enforce and CheckConfig change (Info) and what they
// could not change or want the operator to see (Warn). Either may be nil.
type Logger struct {
	Info func(msg string, fields map[string]any)
	Warn func(msg string, fields map[string]any)
}

func (l Logger) info(msg string, fields map[string]any) {
	if l.Info != nil {
		l.Info(msg, fields)
	}
}

func (l Logger) warn(msg string, fields map[string]any) {
	if l.Warn != nil {
		l.Warn(msg, fields)
	}
}

// secretDirs are the data directory's folders that hold only secrets.
var secretDirs = []string{global.InternalDir, "tls", "tokens"}

// PolicyFor is the policy security.permissions in cfg describes: the root,
// secrets and default modes; the logs, media, common, backup and bin areas;
// the agents area (agents.base_dir and every configured workspace, whose
// databases follow the area's file mode); and the secrets-only folders.
func PolicyFor(cfg *config.Config) Policy {
	p := Policy{
		DataDir: filepath.Clean(cfg.DataDir()),
		Root:    cfg.PermissionModes(config.AreaRoot),
		Secrets: cfg.PermissionModes(config.AreaSecrets),
		Default: cfg.PermissionModes(config.AreaDefault),
	}
	add := func(root, area string, a Area) {
		a.Root, a.Modes = root, cfg.PermissionModes(area)
		p.Areas = append(p.Areas, a)
	}
	dd := cfg.DataDir()
	add(filepath.Join(dd, "logs"), config.AreaLogs, Area{Plain: true})
	add(filepath.Join(dd, "media"), config.AreaMedia, Area{Plain: true})
	add(cfg.ResolveCommonDir(), config.AreaCommon, Area{})
	add(cfg.BackupDir(), config.AreaBackup, Area{})
	add(cfg.BinPath(), config.AreaBin, Area{Bin: true})
	for _, dir := range cfg.AgentDirs() {
		add(dir, config.AreaAgents, Area{Databases: true})
	}
	for _, name := range secretDirs {
		add(filepath.Join(dd, name), config.AreaSecrets, Area{Secret: true})
	}
	return p
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

// dirMode is the mode the policy sets on a directory below the data
// directory.
func (p Policy) dirMode(path string) fs.FileMode {
	if a := p.area(path); a != nil {
		return a.Modes.Dir
	}
	return p.Default.Dir
}

// fileMode is the mode the policy sets on the regular file at path, which
// lies under dataDir. A bin, logs or media file takes its area's mode
// whatever its name; a secret takes the secrets mode; an area's database takes the area's mode
// when the area opts in (the secrets mode otherwise); anything else its
// area's mode, the root mode directly in the data directory, else the
// default mode.
func (p Policy) fileMode(dataDir, path string) fs.FileMode {
	a := p.area(path)
	switch {
	case a != nil && (a.Bin || a.Plain):
		return a.Modes.File
	case a != nil && a.Secret, isSecretFile(path):
		return p.Secrets.File
	case isDatabase(path):
		if a != nil && a.Databases {
			return a.Modes.File
		}
		return p.Secrets.File
	case a != nil:
		return a.Modes.File
	case filepath.Dir(path) == dataDir:
		return p.Root.File
	}
	return p.Default.File
}

// ModesFor returns the modes something new at path takes: Dir if it is a
// folder, File if it is a regular file (by its name and area, as Enforce
// would set it). A path outside every area and outside the data directory
// takes the default area's modes. A zero Policy gives 0700/0600.
func (p Policy) ModesFor(path string) config.Modes {
	if p.Default == (config.Modes{}) {
		return config.Modes{Dir: config.DefaultDirMode, File: config.DefaultFileMode}
	}
	path = filepath.Clean(path)
	m := config.Modes{Dir: p.dirMode(path), File: p.fileMode(p.DataDir, path)}
	if path == p.DataDir {
		m.Dir = p.Root.Dir
	}
	return m
}

// ModesFor is PolicyFor(cfg).ModesFor(path): the modes something new at path
// takes under cfg.
func ModesFor(cfg *config.Config, path string) config.Modes {
	return PolicyFor(cfg).ModesFor(path)
}

// CardModes are the modes the character-card library (cfg.CardsPath) and its
// card files take, for ccv3.WithModes.
func CardModes(cfg *config.Config) config.Modes {
	return ModesFor(cfg, filepath.Join(cfg.CardsPath(), "card.json"))
}

// DirMode is ModesFor(path).Dir, for fileutil.MkdirAllWith.
func (p Policy) DirMode(path string) fs.FileMode { return p.ModesFor(path).Dir }

// MkdirAll creates path and any missing parents, each with exactly the
// folder mode its area gives it (fileutil.MkdirAllWith).
func (p Policy) MkdirAll(path string) error {
	return fileutil.MkdirAllWith(path, p.DirMode)
}

// WriteFile writes data to path atomically with exactly the file mode its
// area gives it, creating missing parent folders with their areas' modes.
func (p Policy) WriteFile(path string, data []byte) error {
	if err := p.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(path, data, p.ModesFor(path).File)
}

// ErrTruncated is returned by Check when the walk hit the entry cap before
// reaching the end of the tree; the findings collected up to then are still
// returned.
var ErrTruncated = errors.New("perms: walk truncated at the entry cap")

// walkLimit bounds the number of directory entries one walk visits. A data
// directory with more is not a normal install and the walk stops rather than
// delaying startup; a variable so tests can lower it.
var walkLimit = 50000

// executable returns the path of the running binary; a variable so tests can
// name a file of their own.
var executable = os.Executable

// runningExecutable stats the running binary, or returns nil when it cannot
// be found: Enforce then has nothing to protect.
func runningExecutable() os.FileInfo {
	path, err := executable()
	if err != nil {
		return nil
	}
	if resolved, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
		path = resolved
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return fi
}

// Enforce sets the data directory and everything in it to exactly the modes
// policy gives them: dataDir (created when missing) to the root folder mode,
// every directory under it to its area's folder mode and every regular file
// to its area's file mode (see Policy.fileMode). Modes are widened as well as
// tightened, and files outside bin/ lose execute bits their mode lacks. A
// data directory that is itself a symbolic link is followed to its target;
// below it symlinks are never followed or changed, an entry swapped for
// another between the walk and the change is left alone (chmodSafe), a file
// with more than one hard link is skipped with a WARN (its other names may
// lie outside the data directory), and the running executable is never
// touched wherever it lies (a WARN names it when it is in the data directory
// outside bin/). Every change is logged at INFO, every failure at WARN; only
// a data directory that cannot be created or read stops the start. On
// Windows it does nothing.
func Enforce(dataDir string, policy Policy, log Logger) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if err := enforceDataDir(dataDir, policy.Root.Dir, log); err != nil {
		return err
	}
	visit := walkVisitor{
		found: func(f Finding) {
			if f.info != nil && f.info.Mode().IsRegular() && linkCount(f.info) > 1 {
				log.warn("file has more than one hard link; its mode is left alone", map[string]any{
					"path": f.Path, "mode": fmt.Sprintf("%04o", f.Mode), "want": fmt.Sprintf("%04o", f.Want),
				})
				return
			}
			setMode(f, log)
		},
		exe: runningExecutable(),
		onExe: func(path string, inBin bool) {
			if !inBin {
				log.warn("the running executable is in the data directory outside bin/; its mode is left alone, move it to bin/",
					map[string]any{"path": path})
			}
		},
	}
	truncated, err := walk(dataDir, policy, visit)
	if err != nil {
		return err
	}
	if truncated {
		log.warn("permission walk stopped at the entry cap; files beyond it were not checked",
			map[string]any{"dir": dataDir, "limit": walkLimit})
	}
	return nil
}

// setMode applies one finding, safely (chmodSafe), and logs it.
func setMode(f Finding, log Logger) {
	target, info := f.real, f.info
	if target == "" {
		target = f.Path
	}
	if info == nil {
		fi, err := os.Lstat(target)
		if err != nil {
			log.warn("could not set file permissions", map[string]any{"path": f.Path, "error": err.Error()})
			return
		}
		info = fi
	}
	if err := chmodSafe(target, f.Want, info); err != nil {
		log.warn("could not set file permissions", map[string]any{
			"path": f.Path, "mode": fmt.Sprintf("%04o", f.Mode), "want": fmt.Sprintf("%04o", f.Want), "error": err.Error(),
		})
		return
	}
	log.info("set file permissions", map[string]any{
		"path": f.Path, "from": fmt.Sprintf("%04o", f.Mode), "to": fmt.Sprintf("%04o", f.Want),
	})
}

// Check is the report-only counterpart of Enforce: it walks the same paths
// with the same rules and returns every path granting a bit its configured
// mode does not (one tighter than configured is not reported), without
// changing anything. dataDir itself is included, and configPath when it lies
// outside dataDir. When the walk hits the entry cap the findings so far are
// returned with ErrTruncated. On Windows it returns nothing.
func Check(dataDir, configPath string, policy Policy) ([]Finding, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	var findings []Finding
	keep := func(f Finding) {
		if f.Looser() {
			findings = append(findings, f)
		}
	}
	if f, ok, err := differs(dataDir, policy.Root.Dir); err != nil && !os.IsNotExist(err) {
		return nil, err
	} else if ok {
		keep(f)
	}
	if configPath != "" && !config.Within(dataDir, configPath) {
		if f, ok, err := differs(configPath, policy.Root.File); err != nil && !os.IsNotExist(err) {
			return nil, err
		} else if ok {
			keep(f)
		}
	}
	truncated, err := walk(dataDir, policy, walkVisitor{found: keep, exe: runningExecutable()})
	if err != nil {
		return nil, err
	}
	if truncated {
		return findings, ErrTruncated
	}
	return findings, nil
}

// secretModes is the process default for the secret files and folders ClawEh
// creates from code that has no configuration in hand (store openers, the pid
// and lock files). Every process sets it from security.permissions.secrets in
// the config it loads: the gateway at start and on every reload, the CLI
// commands through internal.LoadConfig or their own load.
var (
	secretModesMu sync.RWMutex
	secretModes   = config.Modes{Dir: config.DefaultDirMode, File: config.DefaultFileMode}
)

// current is the process default policy for files and folders ClawEh creates
// from code that has no configuration in hand (SetPolicy, CurrentPolicy).
var (
	policyMu sync.RWMutex
	current  Policy
)

// SetPolicy sets the process default policy (CurrentPolicy) and the secrets
// modes (SecretModes) from it. Every process sets it from the config it
// loads: the gateway at start and on every reload, the CLI commands through
// internal.LoadConfig or their own load.
func SetPolicy(p Policy) {
	policyMu.Lock()
	current = p
	policyMu.Unlock()
	SetSecretModes(p.Secrets)
}

// CurrentPolicy returns the policy SetPolicy installed; before that, a zero
// Policy, whose ModesFor gives 0700/0600.
func CurrentPolicy() Policy {
	policyMu.RLock()
	defer policyMu.RUnlock()
	return current
}

// SetSecretModes sets the process default secrets modes (SecretModes). A
// zero mode keeps the current one.
func SetSecretModes(m config.Modes) {
	secretModesMu.Lock()
	defer secretModesMu.Unlock()
	if m.Dir != 0 {
		secretModes.Dir = m.Dir
	}
	if m.File != 0 {
		secretModes.File = m.File
	}
}

// SecretModes returns the modes secret folders and files are created with:
// security.permissions.secrets once the process has set it from the config it
// loaded (SetSecretModes), 0700/0600 before.
func SecretModes() config.Modes {
	secretModesMu.RLock()
	defer secretModesMu.RUnlock()
	return secretModes
}

// EnsureSecretFile is EnsureFileMode with the secrets file mode
// (SecretModes). Store openers call it before sql.Open: SQLite creates a new
// database with the umask default (0644) and gives the -wal and -shm side
// files the main file's mode, so a main file with the secrets mode gives
// them that mode too, from the first write. An empty file is a valid empty
// SQLite database.
func EnsureSecretFile(path string) error {
	return EnsureFileMode(path, SecretModes().File)
}

// EnsureFileMode creates path as an empty file when it does not exist and
// sets it to exactly mode, whatever the umask, acting on the open descriptor.
// A symbolic link is refused. On Windows it does nothing.
func EnsureFileMode(path string, mode fs.FileMode) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	// A symbolic link is refused, never followed: creating or changing
	// through it would act on a file somewhere else.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("perms: %s is a symbolic link", path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY|oNoFollow, mode.Perm()) //nolint:gosec // the path the caller is about to open
	if err != nil {
		return fmt.Errorf("perms: create %s: %w", path, err)
	}
	err = setOpen(f, mode.Perm())
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("perms: %s: %w", path, err)
	}
	return nil
}

// setOpen sets the open regular file f to exactly mode, acting on the
// descriptor so no path is resolved again.
func setOpen(f *os.File, mode fs.FileMode) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if fi.Mode().Perm() != mode {
		return f.Chmod(mode)
	}
	return nil
}

// enforceDataDir creates dataDir with dirMode when missing and sets it (its
// target, when it is a symbolic link) to exactly dirMode. A failed chmod (a
// directory owned by someone else) is logged, not fatal: the operator can
// still run.
func enforceDataDir(dataDir string, dirMode fs.FileMode, log Logger) error {
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(dataDir, dirMode); mkErr != nil {
			return fmt.Errorf("perms: create %s: %w", dataDir, mkErr)
		}
	}
	resolved, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return fmt.Errorf("perms: resolve %s: %w", dataDir, err)
	}
	fi, err := os.Lstat(resolved)
	if err != nil {
		return fmt.Errorf("perms: stat %s: %w", dataDir, err)
	}
	if mode := fi.Mode().Perm(); mode != dirMode.Perm() {
		setMode(Finding{Path: dataDir, Mode: mode, Want: dirMode.Perm(), real: resolved, info: fi}, log)
	}
	return nil
}

// CheckConfig holds the config file to mode (security.permissions.root.
// file_mode). A file granting any bit mode does not is a refusal to start,
// naming the chmod that fixes it: it is never widened or narrowed on the
// operator's behalf, since a loose config is a sign something else on the
// host has been at it. A tighter file is set to exactly mode (unless it is a
// symbolic link, which is never changed). A missing config is fine (first run
// seeds one). On Windows it does nothing.
func CheckConfig(configPath string, mode fs.FileMode, log Logger) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	mode = mode.Perm()
	f, ok, err := differs(configPath, mode)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("perms: stat %s: %w", configPath, err)
	}
	if !ok {
		return nil
	}
	if f.Looser() {
		return fmt.Errorf("perms: %s has mode %04o, more than security.permissions.root.file_mode allows (%04o); it holds credentials, so refusing to start. Fix with: chmod %03o %s",
			configPath, f.Mode, mode, mode, configPath)
	}
	fi, err := os.Lstat(configPath)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return nil //nolint:nilerr // a link (or a file gone since the stat) is not changed
	}
	f.info = fi
	setMode(f, log)
	return nil
}

// differs stats path (following a symlink, since it is the target's bits
// that matter) and reports whether its mode differs from want.
func differs(path string, want fs.FileMode) (Finding, bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Finding{}, false, err
	}
	if mode := fi.Mode().Perm(); mode != want.Perm() {
		return Finding{Path: path, Mode: mode, Want: want.Perm()}, true, nil
	}
	return Finding{}, false, nil
}

// walkVisitor is what walk calls: found for each path whose mode differs
// from its configured one, onExe (optional) when it meets the running
// executable exe (nil: none), which is never reported through found.
type walkVisitor struct {
	found func(Finding)
	exe   os.FileInfo
	onExe func(path string, inBin bool)
}

// walk visits every directory and regular file under dataDir, calling
// visit.found for each whose mode differs from the one policy gives it. A
// data directory that is a symbolic link is walked at its target, while the
// policy is matched against the configured spelling. The secrets folders and
// the files directly in the data directory are walked first, each with its
// own entry cap, so a large agents tree cannot use up the cap before they
// are reached. It reports whether any part stopped at walkLimit. Unreadable
// subtrees are skipped, not fatal; symlinks are skipped.
func walk(dataDir string, policy Policy, visit walkVisitor) (bool, error) {
	dataDir = filepath.Clean(dataDir)
	resolved, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("perms: resolve %s: %w", dataDir, err)
	}
	t := treeWalk{logical: dataDir, real: resolved, policy: policy, visit: visit}

	// 1. The files directly in the data directory.
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return false, fmt.Errorf("perms: walk %s: %w", dataDir, err)
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			t.entry(filepath.Join(resolved, e.Name()), e)
		}
	}
	// 2. Each secrets folder, then 3. everything else.
	truncated := false
	for _, name := range secretDirs {
		cut, subErr := t.subtree(filepath.Join(resolved, name), nil)
		if subErr != nil {
			return false, subErr
		}
		truncated = truncated || cut
	}
	skip := map[string]bool{}
	for _, name := range secretDirs {
		skip[filepath.Join(resolved, name)] = true
	}
	cut, err := t.subtree(resolved, skip)
	if err != nil {
		return false, err
	}
	return truncated || cut, nil
}

// treeWalk is one walk of a data directory: logical is its configured
// spelling, real the directory it resolves to.
type treeWalk struct {
	logical, real string
	policy        Policy
	visit         walkVisitor
}

// subtree walks root (under t.real) with its own entry cap, leaving out the
// directories in skip and, at the data directory itself, the regular files
// walk visited first. A missing root is fine.
func (t treeWalk) subtree(root string, skip map[string]bool) (bool, error) {
	seen := 0
	truncated := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			// An unreadable entry below the root is skipped, not fatal: one
			// odd subtree must not stop the rest of the walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == t.real {
			return nil // the data directory itself: enforceDataDir's
		}
		if skip[path] {
			return fs.SkipDir
		}
		if filepath.Dir(path) == t.real && d.Type().IsRegular() {
			return nil // visited first
		}
		seen++
		if seen > walkLimit {
			truncated = true
			return fs.SkipAll
		}
		t.entry(path, d)
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("perms: walk %s: %w", t.logical, err)
	}
	return truncated, nil
}

// entry checks one walked entry at real path p against the policy.
func (t treeWalk) entry(p string, d fs.DirEntry) {
	if d.Type()&fs.ModeSymlink != 0 || (!d.IsDir() && !d.Type().IsRegular()) {
		return
	}
	info, err := d.Info()
	if err != nil {
		return // the entry vanished mid-walk (a WAL side file, say); nothing to set
	}
	logical := t.logical
	if rel, relErr := filepath.Rel(t.real, p); relErr == nil && rel != "." {
		logical = filepath.Join(t.logical, rel)
	}
	var want fs.FileMode
	if d.IsDir() {
		want = t.policy.dirMode(logical)
	} else {
		if t.visit.exe != nil && os.SameFile(info, t.visit.exe) {
			if t.visit.onExe != nil {
				a := t.policy.area(logical)
				t.visit.onExe(logical, a != nil && a.Bin)
			}
			return
		}
		want = t.policy.fileMode(t.logical, logical)
	}
	if mode := info.Mode().Perm(); mode != want.Perm() {
		t.visit.found(Finding{Path: logical, Mode: mode, Want: want.Perm(), real: p, info: info})
	}
}

// isSecretFile decides by name whether a file outside the secrets folders
// holds secrets: credentials.json and any file whose name mentions a token or
// a secret.
func isSecretFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return name == "credentials.json" || strings.Contains(name, "token") || strings.Contains(name, "secret")
}

// isDatabase reports by name whether the file is a SQLite database or one of
// its WAL/shm side files.
func isDatabase(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	ext := filepath.Ext(name)
	return ext == ".db" || ext == ".db-wal" || ext == ".db-shm" || strings.Contains(name, ".sqlite")
}
