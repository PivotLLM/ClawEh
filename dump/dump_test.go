// ClawEh
// License: MIT

package dump

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Dumps hold raw LLM input and output, so the dumps directory and both files
// of a pair are owner-only.
func TestWrite_OwnerOnlyModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}
	dir := filepath.Join(t.TempDir(), "dumps")
	base, err := Write(dir, "test", map[string]any{"k": "v"},
		json.RawMessage(`{"in":1}`), json.RawMessage(`{"out":2}`))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if base == "" {
		t.Fatal("Write returned an empty basename")
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dumps dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("dumps dir mode = %04o, want 0700", got)
	}
	for _, ext := range []string{".json", ".txt"} {
		fi, err := os.Stat(filepath.Join(dir, base+ext))
		if err != nil {
			t.Fatalf("stat %s: %v", base+ext, err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", base+ext, got)
		}
	}
}
