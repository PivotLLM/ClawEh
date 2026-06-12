package templates

import "testing"

// TestEmbeddedFiles guards that the default workspace template files — including
// the default compression profile and the skills marker that keeps go:embed
// happy for the (intentionally empty) skills dir — stay embedded.
func TestEmbeddedFiles(t *testing.T) {
	for _, f := range []string{
		"AGENTS.md",
		"IDENTITY.md",
		"COMPRESSION.md",
		"memory/MEMORY.md",
		"skills/.gitkeep",
	} {
		if _, err := FS.ReadFile(f); err != nil {
			t.Errorf("expected %q embedded in templates.FS: %v", f, err)
		}
	}
}
