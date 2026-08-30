// ClawEh
// License: MIT

package servicetoken

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerate_Format(t *testing.T) {
	tok, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(tok, "SST") {
		t.Errorf("token missing SST prefix: %q", tok)
	}
	if len(tok) != 3+64 {
		t.Errorf("token length = %d, want %d", len(tok), 3+64)
	}
	other, _ := Generate()
	if tok == other {
		t.Error("Generate returned identical tokens")
	}
}

func TestLoad_MissingFileIsEmpty(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load(missing): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Load(missing) = %v, want empty", got)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	path := Path(t.TempDir())
	in := map[string]string{"amber": "SSTaaa", "dawn": "SSTbbb"}
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Verify perms are tight (0600).
	if fi, err := os.Stat(path); err == nil {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("state file perm = %o, want 600", perm)
		}
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out) != 2 || out["amber"] != "SSTaaa" || out["dawn"] != "SSTbbb" {
		t.Errorf("round-trip mismatch: %v", out)
	}
}

func TestAgents_SortedNoTokens(t *testing.T) {
	ids := Agents(map[string]string{"zeb": "x", "amber": "y", "dawn": "z"})
	want := []string{"amber", "dawn", "zeb"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("Agents = %v, want %v", ids, want)
	}
}
