// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package layout prepares the data directory (CLAW_HOME) at start: it creates
// the directories claw owns and moves files from the older layout into them.
//
// The layout, by purpose:
//
//	internal/  claw's own state nobody edits (state.json, token stores, the device database)
//	cli/       working directory for CLI providers whose model sets no workspace
//	skills/    shared skills every agent can use
//	common/    shared directory for the common_* tools (unless agents.common_dir is set)
//
// The older layout kept claw's state in <CLAW_HOME>/state and
// <agents base>/default/state, shared skills also in <agents base>/default/skills,
// and the common directory in <agents base>/common. Prepare moves each of those
// once, then removes <agents base>/default, unless a configured agent uses it
// as its workspace (then only claw's state.json is copied out).
package layout

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/utils"
)

const (
	dirMode = 0o700

	// legacyStateDir is the old <CLAW_HOME>/state directory.
	legacyStateDir = "state"
	// legacyDefaultDir is the old <agents base>/default pseudo-agent directory.
	legacyDefaultDir = "default"
	// stateFile is claw's own state file.
	stateFile = "state.json"
)

// Prepare creates internal/, cli/, skills/ and the common directory under the
// data directory, moving files from the older layout first. It must run once
// at start, before anything opens those files. It is idempotent: a second run
// finds nothing to move. Problems are logged; none stops the start.
func Prepare(cfg *config.Config) {
	dataDir := cfg.DataDir()
	if dataDir == "" {
		return
	}
	internalDir := cfg.InternalPath()
	skillsDir := cfg.SkillsPath()
	for _, dir := range []string{internalDir, cfg.CLIPath(), skillsDir} {
		mkdir(dir)
	}

	moveLegacyState(filepath.Join(dataDir, legacyStateDir), internalDir)
	moveLegacyCommon(cfg)
	mkdir(cfg.ResolveCommonDir())
	moveLegacyDefault(cfg, internalDir, skillsDir)
}

// moveLegacyState moves every entry of the old <CLAW_HOME>/state into
// internal/, then removes the old directory when nothing is left in it. An
// entry that already exists in internal/ is left where it is.
func moveLegacyState(oldDir, internalDir string) {
	entries, err := os.ReadDir(oldDir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		logger.WarnCF("layout", "Cannot read the old state directory", map[string]any{"path": oldDir, "error": err.Error()})
		return
	}
	var kept []string
	for _, e := range entries {
		src := filepath.Join(oldDir, e.Name())
		dst := filepath.Join(internalDir, e.Name())
		if exists(dst) {
			kept = append(kept, e.Name())
			continue
		}
		if err := move(src, dst); err != nil {
			logger.WarnCF("layout", "Cannot move a state file", map[string]any{"from": src, "to": dst, "error": err.Error()})
			kept = append(kept, e.Name())
		}
	}
	if len(kept) > 0 {
		logger.WarnCF("layout", "Some files in the old state directory were left in place because internal already has them",
			map[string]any{"path": oldDir, "files": strings.Join(kept, ", ")})
		return
	}
	if err := os.Remove(oldDir); err != nil {
		logger.WarnCF("layout", "Cannot remove the old state directory", map[string]any{"path": oldDir, "error": err.Error()})
		return
	}
	logger.InfoCF("layout", "Moved the old state directory into internal", map[string]any{"from": oldDir, "to": internalDir})
}

// moveLegacyCommon moves <agents base>/common to <CLAW_HOME>/common when the
// common directory is the default one, the old directory exists, and the new
// one does not.
func moveLegacyCommon(cfg *config.Config) {
	if cfg.Agents.CommonDir != "" {
		return
	}
	oldDir := filepath.Join(cfg.BaseDir(), global.CommonDir)
	newDir := cfg.ResolveCommonDir()
	if samePath(oldDir, newDir) || !isDir(oldDir) || exists(newDir) {
		return
	}
	if id := agentUsing(cfg, oldDir); id != "" {
		logger.WarnCF("layout", "The old common directory is an agent's workspace, so it was not moved",
			map[string]any{"path": oldDir, "agent": id})
		return
	}
	if err := move(oldDir, newDir); err != nil {
		logger.WarnCF("layout", "Cannot move the common directory", map[string]any{"from": oldDir, "to": newDir, "error": err.Error()})
		return
	}
	logger.InfoCF("layout", "Moved the common directory", map[string]any{"from": oldDir, "to": newDir})
}

// moveLegacyDefault moves claw's state.json and the shared skills out of
// <agents base>/default and removes that directory. A skill whose name the
// shared root already has is moved under a new name. When a configured agent
// uses the directory as its workspace, only claw's state.json is copied out
// (the agent keeps reading its own copy) and everything else is left alone.
func moveLegacyDefault(cfg *config.Config, internalDir, skillsDir string) {
	oldDir := filepath.Join(cfg.BaseDir(), legacyDefaultDir)
	if !isDir(oldDir) {
		return
	}
	dst := filepath.Join(internalDir, stateFile)
	if id := agentUsing(cfg, oldDir); id != "" {
		src := filepath.Join(oldDir, legacyStateDir, stateFile)
		if !exists(dst) && exists(src) {
			if err := copyAny(src, dst); err != nil {
				logger.WarnCF("layout", "Cannot copy state.json", map[string]any{"from": src, "to": dst, "error": err.Error()})
			}
		}
		logger.WarnCF("layout", "agents/default is an agent's workspace, so it was left in place",
			map[string]any{"path": oldDir, "agent": id})
		return
	}

	for _, src := range []string{
		filepath.Join(oldDir, legacyStateDir, stateFile),
		filepath.Join(oldDir, stateFile), // before the state/ subdirectory existed
	} {
		if exists(dst) {
			break
		}
		if !exists(src) {
			continue
		}
		if err := move(src, dst); err != nil {
			logger.WarnCF("layout", "Cannot move state.json", map[string]any{"from": src, "to": dst, "error": err.Error()})
			return
		}
		logger.InfoCF("layout", "Moved state.json into internal", map[string]any{"from": src, "to": dst})
	}

	renamed, err := moveSkills(filepath.Join(oldDir, "skills"), skillsDir)
	if err != nil {
		logger.WarnCF("layout", "Cannot move the skills from agents/default, so it was left in place",
			map[string]any{"path": oldDir, "error": err.Error()})
		return
	}
	if len(renamed) > 0 {
		logger.WarnCF("layout", "Some skills from agents/default had the name of a shared skill and were renamed",
			map[string]any{"skills": strings.Join(renamed, ", ")})
	}

	if err := os.RemoveAll(oldDir); err != nil {
		logger.WarnCF("layout", "Cannot remove agents/default", map[string]any{"path": oldDir, "error": err.Error()})
		return
	}
	logger.InfoCF("layout", "Removed agents/default, which claw no longer uses", map[string]any{"path": oldDir})
}

// moveSkills moves each entry of oldDir into newDir. An entry whose name
// newDir already has is moved as <name>-default, or <name>-default-2 and so
// on; those are returned as "old -> new".
func moveSkills(oldDir, newDir string) ([]string, error) {
	entries, err := os.ReadDir(oldDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var renamed []string
	for _, e := range entries {
		src := filepath.Join(oldDir, e.Name())
		name := e.Name()
		if exists(filepath.Join(newDir, name)) {
			name = e.Name() + "-default"
			for n := 2; exists(filepath.Join(newDir, name)); n++ {
				name = fmt.Sprintf("%s-default-%d", e.Name(), n)
			}
			renamed = append(renamed, e.Name()+" -> "+name)
		}
		dst := filepath.Join(newDir, name)
		if err := move(src, dst); err != nil {
			return renamed, fmt.Errorf("move %s: %w", src, err)
		}
		logger.InfoCF("layout", "Moved a skill into the shared skills", map[string]any{"from": src, "to": dst})
	}
	return renamed, nil
}

// agentUsing returns the id of a configured agent whose workspace is dir, or
// "" when none is. Workspaces resolve as the agent loop resolves them: an
// explicit workspace, else <agents base>/<id>, where an empty id or "main"
// means "default".
func agentUsing(cfg *config.Config, dir string) string {
	base := cfg.BaseDir()
	for _, ac := range cfg.Agents.List {
		ws := strings.TrimSpace(ac.Workspace)
		if ws != "" {
			ws = expandHome(ws)
		} else {
			id := legacyDefaultDir
			if nid := routing.NormalizeAgentID(ac.ID); nid != "" && nid != "main" {
				id = nid
			}
			ws = filepath.Join(base, id)
		}
		if samePath(ws, dir) {
			if ac.ID == "" {
				return "(no id)"
			}
			return ac.ID
		}
	}
	return ""
}

// move renames src to dst, copying and then removing src when they are on
// different filesystems (agents.base_dir may be another volume).
func move(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyAny(src, dst); err != nil {
		if rmErr := os.RemoveAll(dst); rmErr != nil {
			logger.WarnCF("layout", "Cannot remove a partial copy", map[string]any{"path": dst, "error": rmErr.Error()})
		}
		return err
	}
	return os.RemoveAll(src)
}

// copyAny copies a file, symlink or directory tree from src to dst, keeping
// modes. Symlinks are recreated, not followed.
func copyAny(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		return copyTree(src, dst, info.Mode().Perm())
	case info.Mode()&fs.ModeSymlink != 0:
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	case info.Mode().IsRegular():
		in, err := os.Open(src) //nolint:gosec // a file inside claw's own data directory
		if err != nil {
			return err
		}
		defer utils.CloseQuietly(in)
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()) //nolint:gosec // a path inside claw's own data directory
		if err != nil {
			return err
		}
		return copyInto(out, in)
	default:
		return nil // sockets, devices: nothing claw keeps
	}
}

// copyTree copies the directory src to dst through os.Root handles, so no
// path in the tree can lead outside either directory.
func copyTree(src, dst string, mode fs.FileMode) error {
	if err := os.Mkdir(dst, mode); err != nil {
		return err
	}
	srcRoot, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer utils.CloseQuietly(srcRoot)
	dstRoot, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer utils.CloseQuietly(dstRoot)
	fsys := srcRoot.FS()
	return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return dstRoot.Mkdir(path, info.Mode().Perm())
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := fs.ReadLink(fsys, path)
			if err != nil {
				return err
			}
			return dstRoot.Symlink(link, path)
		case info.Mode().IsRegular():
			in, err := fsys.Open(path)
			if err != nil {
				return err
			}
			defer utils.CloseQuietly(in)
			out, err := dstRoot.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
			if err != nil {
				return err
			}
			return copyInto(out, in)
		default:
			return nil
		}
	})
}

// copyInto copies in to out and closes out, reporting a failed close.
func copyInto(out *os.File, in io.Reader) error {
	if _, err := io.Copy(out, in); err != nil {
		utils.CloseQuietly(out)
		return err
	}
	return out.Close()
}

func mkdir(dir string) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		logger.WarnCF("layout", "Cannot create a data directory", map[string]any{"path": dir, "error": err.Error()})
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func isDir(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.IsDir()
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}
