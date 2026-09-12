// ClawEh - Cognitive Memory
// License: MIT

package consolidate_test

import (
	"os"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/cogmem/consolidate"
	"github.com/PivotLLM/ClawEh/templates"
)

// The seeded COGMEM.md must NOT be a copy of the engine prompt.
//
// It used to be required to be an exact copy, because the file replaced the
// prompt wholesale. It now ADDS to it — so a copy would show the model two
// contradictory output schemas, and BuildPrompt therefore ignores any file
// carrying the engine markers. Seeding one would mean every new workspace was
// born with a file that is silently discarded and warned about, which is a
// confusing thing to hand somebody.
func TestSeededTemplateIsNotACopyOfThePrompt(t *testing.T) {
	seeded, err := templates.FS.ReadFile("COGMEM.md")
	if err != nil {
		t.Fatalf("read templates/COGMEM.md: %v", err)
	}
	text := string(seeded)

	if text == consolidate.DefaultPrompt() {
		t.Fatal("templates/COGMEM.md is a copy of the engine prompt; it would be " +
			"ignored on every freshly-seeded agent")
	}
	for _, marker := range []string{"# OUTPUT SCHEMA", "# CORE RULES"} {
		if strings.Contains(text, marker) {
			t.Errorf("templates/COGMEM.md contains %q, so BuildPrompt would ignore it", marker)
		}
	}

	// And prove it round-trips: seeding this file into a workspace must produce
	// a prompt that is accepted, not discarded.
	if _, res := consolidate.BuildPrompt(writeTemp(t, text)); res.Ignored {
		t.Errorf("the seeded template is ignored by BuildPrompt: %s", res.Reason)
	}
}

// The seeded file is guidance, so an agent that never has it edited must behave
// exactly as if it had no file at all.
func TestSeededTemplateIsInertUntilEdited(t *testing.T) {
	seeded, err := templates.FS.ReadFile("COGMEM.md")
	if err != nil {
		t.Fatalf("read templates/COGMEM.md: %v", err)
	}
	got, _ := consolidate.BuildPrompt(writeTemp(t, string(seeded)))
	if n := strings.Count(got, "# OUTPUT SCHEMA"); n != 1 {
		t.Errorf("prompt has %d output schemas, want exactly 1", n)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := t.TempDir() + "/COGMEM.md"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// The seeded file explains itself to the OPERATOR, and that explanation must
// not reach the model. An unedited workspace would otherwise add several
// hundred characters of "write your instructions below" to every consolidation
// prompt — addressed to the wrong reader, in a prompt whose point is precision.
func TestSeededTemplateAddsNothingToThePrompt(t *testing.T) {
	seeded, err := templates.FS.ReadFile("COGMEM.md")
	if err != nil {
		t.Fatalf("read templates/COGMEM.md: %v", err)
	}
	got, res := consolidate.BuildPrompt(writeTemp(t, string(seeded)))
	if res.Appended {
		t.Error("the unedited seeded file was appended to the prompt")
	}
	if got != consolidate.DefaultPrompt() {
		t.Errorf("an unedited workspace changes the prompt (%d vs %d chars)",
			len(got), len(consolidate.DefaultPrompt()))
	}
}

// Writing below the comment block is what turns it on.
func TestInstructionsBelowTheCommentAreUsed(t *testing.T) {
	seeded, err := templates.FS.ReadFile("COGMEM.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	edited := string(seeded) + "\nNever record anything about medical matters.\n"
	got, res := consolidate.BuildPrompt(writeTemp(t, edited))
	if !res.Appended {
		t.Fatal("instructions written below the comment block were ignored")
	}
	if !strings.Contains(got, "Never record anything about medical matters.") {
		t.Error("the instruction is missing from the prompt")
	}
	if strings.Contains(got, "write your instructions below") {
		t.Error("operator guidance leaked into the prompt alongside the instruction")
	}
}
