// ClawEh - Cognitive Memory
// License: MIT

package consolidate

import (
	_ "embed"
	"os"
	"path/filepath"
)

// defaultPrompt is the built-in consolidation system prompt. The editable
// per-agent copy is seeded into each workspace as COGMEM.md from
// templates/COGMEM.md (identical content); keep the two in sync.
//
//go:embed default_prompt.md
var defaultPrompt string

// DefaultPrompt returns the embedded default consolidation prompt.
func DefaultPrompt() string { return defaultPrompt }

// PromptPath returns the per-agent consolidation prompt path for a workspace.
func PromptPath(workspace string) string { return filepath.Join(workspace, PromptFilename) }

// LoadPrompt returns the consolidation system prompt: the file at path if set
// and non-empty, otherwise the embedded default. usedOverride reports whether
// the override file was used (false means the default was used, including when
// an override path was set but unreadable — the caller should log that).
func LoadPrompt(path string) (prompt string, usedOverride bool) {
	if path != "" {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return string(b), true
		}
	}
	return defaultPrompt, false
}
