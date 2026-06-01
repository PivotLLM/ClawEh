// Package workspace manages agent workspace initialization.
package workspace

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/templates"
)

// Populate writes the default workspace template files into dir, skipping any
// file that already exists. This makes it safe to call on every startup —
// existing agent customizations are never overwritten.
func Populate(dir string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.WarnCF("workspace", "Failed to create workspace directory",
			map[string]any{"dir": dir, "error": err.Error()})
		return
	}

	err := fs.WalkDir(templates.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, path), 0o755)
		}
		dest := filepath.Join(dir, path)
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
			map[string]any{"dir": dir, "error": err.Error()})
	}
}
