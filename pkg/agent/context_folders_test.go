// ClawEh
// License: MIT

package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func TestAccessibleFolders(t *testing.T) {
	cb := &ContextBuilder{}
	if got := cb.accessibleFolders(); got != "files/ (read/write), skills/ (read-only)" {
		t.Fatalf("base folders = %q", got)
	}

	cb.mounts = []config.MountConfig{{Name: "notes"}, {Name: ""}, {Name: "data"}}
	want := "files/ (read/write), skills/ (read-only), notes/ (external, read/write), data/ (external, read/write)"
	if got := cb.accessibleFolders(); got != want {
		t.Fatalf("with mounts = %q, want %q", got, want)
	}
}
