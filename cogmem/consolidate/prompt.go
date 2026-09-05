// ClawEh - Cognitive Memory
// License: MIT

package consolidate

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
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

// contractMarkers are vocabulary the current contract requires the prompt to
// teach. A prompt missing them predates the memory-type redesign.
var contractMarkers = []string{"event", "operational"}

// PromptIsStale reports whether an override prompt predates the current
// contract, and is deliberately conservative: it only looks for vocabulary the
// current prompt cannot do without.
//
// This matters because the override is seeded into every workspace and then
// never overwritten — that is the point of it, but it means an install that
// upgrades keeps whatever prompt it was seeded with. A stale one still produces
// VALID output (it names types that still exist), so nothing rejects it and
// nothing fails. It simply never writes an `event` or `operational` memory, and
// the operator has no way to notice that the most useful part of their upgrade
// is not reaching that agent. Hence a warning rather than silence.
func PromptIsStale(prompt string) bool {
	for _, m := range contractMarkers {
		if !strings.Contains(prompt, m) {
			return true
		}
	}
	return false
}
