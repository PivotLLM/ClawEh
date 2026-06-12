// Package workspace manages agent workspace initialization.
package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/templates"
)

// Populate writes the default workspace template files into workspace, skipping
// any file that already exists. It is safe to call on every startup — existing
// agent customizations are never overwritten.
//
// Two template categories are handled specially so a personalized agent is not
// disturbed on restart:
//
//   - BOOTSTRAP.md is a one-time setup file the agent deletes after
//     personalization. It is seeded ONLY into a brand-new (uninitialized)
//     workspace, never re-created afterwards.
//   - The memory templates are seeded into the agent's resolved memory
//     directory (which may be relocated via config), and only for a brand-new
//     workspace. They are never written into <workspace>/memory when memory is
//     relocated, so a deleted/relocated memory directory does not reappear.
//
// memoryDir is the agent's resolved memory directory; empty means the default
// <workspace>/memory layout.
func Populate(workspace, memoryDir string) {
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		logger.WarnCF("workspace", "Failed to create workspace directory",
			map[string]any{"dir": workspace, "error": err.Error()})
		return
	}

	// A workspace is "initialized" once its core identity file exists. On an
	// initialized workspace we must not recreate one-time or user-deletable
	// files: otherwise a personalized agent would be told to bootstrap again on
	// every restart, and a deleted (or relocated-away) memory directory would
	// silently reappear.
	initialized := fileExists(filepath.Join(workspace, "AGENTS.md"))

	err := fs.WalkDir(templates.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Memory templates are seeded separately into the resolved memory
		// directory, never unconditionally into <workspace>/memory.
		if path == "memory" || strings.HasPrefix(path, "memory/") {
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(workspace, path), 0o755)
		}
		// BOOTSTRAP.md is a one-time setup file: seed it only into a brand-new
		// workspace, never re-create it after the agent is initialized.
		if path == "BOOTSTRAP.md" && initialized {
			return nil
		}
		// COMPRESSION.md is an optional, user-deletable summarization profile.
		// Seed it only into a brand-new workspace so deleting it (to use the
		// built-in behavior) sticks, and so an existing customised compression
		// profile is never clobbered.
		if path == "COMPRESSION.md" && initialized {
			return nil
		}
		dest := filepath.Join(workspace, path)
		if _, statErr := os.Stat(dest); statErr == nil {
			return nil // already exists — preserve user customisation
		}
		data, err := templates.FS.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})

	if err != nil {
		logger.WarnCF("workspace", "Failed to populate workspace templates",
			map[string]any{"dir": workspace, "error": err.Error()})
	}

	// Seed the starter memory only for a brand-new workspace, into the resolved
	// memory directory. Skipping this on an already-initialized workspace is
	// what prevents a deleted/relocated memory directory from reappearing.
	if !initialized {
		seedMemory(workspace, memoryDir)
	}
}

// seedMemory copies the embedded memory templates into the resolved memory
// directory (memoryDir, or <workspace>/memory when empty), skipping any file
// that already exists.
func seedMemory(workspace, memoryDir string) {
	target := strings.TrimSpace(memoryDir)
	if target == "" {
		target = filepath.Join(workspace, "memory")
	}

	err := fs.WalkDir(templates.FS, "memory", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(path, "memory"), "/")
		dest := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o700)
		}
		if _, statErr := os.Stat(dest); statErr == nil {
			return nil
		}
		data, err := templates.FS.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o600)
	})

	if err != nil {
		logger.WarnCF("workspace", "Failed to seed memory templates",
			map[string]any{"dir": target, "error": err.Error()})
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
