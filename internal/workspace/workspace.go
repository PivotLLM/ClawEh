// Package workspace manages agent workspace initialization.
package workspace

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/templates"
)

// Populate writes the default workspace template files into workspace, skipping
// any file that already exists. It is safe to call on every startup — existing
// agent customizations are never overwritten.
//
// BOOTSTRAP.md, COMPRESSION.md, and MEMORY.md are user-deletable: they are seeded
// only into a brand-new (uninitialized) workspace, so deleting one sticks and a
// personalized agent is not disturbed on restart.
func Populate(workspace string) {
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		logger.WarnCF("workspace", "Failed to create workspace directory",
			map[string]any{"dir": workspace, "error": err.Error()})
		return
	}

	// The agent's writable area is <workspace>/files (the read-only-workspace
	// default; see AgentDefaults.WorkspaceWriteSubdir). Ensure it always exists
	// so the agent has somewhere to write from first run.
	if err := os.MkdirAll(filepath.Join(workspace, "files"), 0o755); err != nil {
		logger.WarnCF("workspace", "Failed to create workspace files directory",
			map[string]any{"dir": filepath.Join(workspace, "files"), "error": err.Error()})
	}

	// A workspace is "initialized" once its core identity file exists. On an
	// initialized workspace we must not recreate user-deletable files, or a
	// deleted BOOTSTRAP/COMPRESSION/MEMORY file would silently reappear.
	initialized := fileExists(filepath.Join(workspace, "AGENTS.md"))

	// Seeded only into a brand-new workspace (so deleting one sticks).
	seedOnce := map[string]bool{
		"BOOTSTRAP.md":   true,
		"COMPRESSION.md": true,
		"MEMORY.md":      true,
	}

	err := fs.WalkDir(templates.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(workspace, path), 0o755)
		}
		if seedOnce[path] && initialized {
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
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
