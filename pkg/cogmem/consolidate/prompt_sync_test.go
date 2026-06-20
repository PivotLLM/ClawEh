// ClawEh - Cognitive Memory
// License: MIT

package consolidate_test

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/cogmem/consolidate"
	"github.com/PivotLLM/ClawEh/templates"
)

// TestSeededTemplateMatchesDefaultPrompt guards the invariant that the two
// embedded copies of the consolidation prompt stay identical: templates/COGMEM.md
// (seeded into each agent workspace by internal/workspace.Populate) and
// pkg/cogmem/consolidate/default_prompt.md (the runtime fallback returned by
// DefaultPrompt when a workspace has no COGMEM.md). Drift between them means a
// freshly-seeded agent and an unseeded one run different prompts — exactly the
// bug this test prevents.
func TestSeededTemplateMatchesDefaultPrompt(t *testing.T) {
	seeded, err := templates.FS.ReadFile("COGMEM.md")
	if err != nil {
		t.Fatalf("read templates/COGMEM.md: %v", err)
	}
	if string(seeded) != consolidate.DefaultPrompt() {
		t.Error("templates/COGMEM.md and the embedded default_prompt.md have drifted; keep them identical")
	}
}
