// ClawEh
// License: MIT

package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

func TestAccessibleFolders(t *testing.T) {
	cb := &ContextBuilder{}
	if got := cb.accessibleFolders(); got != "files/ (read/write), skills/ (read-only)" {
		t.Fatalf("base folders = %q", got)
	}

	// Mounts default to read-only; only Writable mounts show read/write.
	// Maestro is in-workspace, so it is not labeled "external".
	cb.mounts = []config.MountConfig{
		{Name: "notes"},
		{Name: ""},
		{Name: "data", Writable: true},
		{Name: "maestro", Writable: true},
	}
	want := "files/ (read/write), skills/ (read-only), notes/ (external, read-only), data/ (external, read/write), maestro/ (read/write)"
	if got := cb.accessibleFolders(); got != want {
		t.Fatalf("with mounts = %q, want %q", got, want)
	}
}
