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
	other, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
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
	in := map[string]string{"amber": Hash("SSTaaa"), "dawn": Hash("SSTbbb")}
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
	if len(out) != 2 || out["amber"] != Hash("SSTaaa") || out["dawn"] != Hash("SSTbbb") {
		t.Errorf("round-trip mismatch: %v", out)
	}
}

// TestHash_StoredFormIsNotTheToken pins what goes to disk: a prefixed SHA-256,
// never the token, and the same token always hashes the same way.
func TestHash_StoredFormIsNotTheToken(t *testing.T) {
	tok, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	h := Hash(tok)
	if !strings.HasPrefix(h, "sha256:") || len(h) != len("sha256:")+64 {
		t.Fatalf("Hash form = %q, want sha256:<64 hex>", h)
	}
	if strings.Contains(h, tok) || strings.Contains(h, tok[3:]) {
		t.Fatalf("hash %q contains the token", h)
	}
	if Hash(tok) != h {
		t.Error("Hash is not deterministic")
	}
}

// TestLoad_MigratesPlaintextFileOnce covers the upgrade from a release that
// stored the tokens themselves: the first Load rewrites the file with hashes
// (0600), returns them, and a second Load leaves the file untouched.
func TestLoad_MigratesPlaintextFileOnce(t *testing.T) {
	path := Path(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	plain := map[string]string{"amber": "SST" + strings.Repeat("ab", 32), "dawn": "SST" + strings.Repeat("cd", 32)}
	if err := os.WriteFile(path, []byte(`{"amber":"`+plain["amber"]+`","dawn":"`+plain["dawn"]+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(plaintext file): %v", err)
	}
	for id, tok := range plain {
		if got[id] != Hash(tok) {
			t.Errorf("Load()[%s] = %q, want Hash(token) %q", id, got[id], Hash(tok))
		}
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for id, tok := range plain {
		if strings.Contains(string(onDisk), tok) {
			t.Errorf("plaintext token for %s still on disk after Load", id)
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("migrated file perm = %o, want 600", perm)
	}

	// Idempotent: a second Load returns the same map and does not rewrite.
	before := fi.ModTime()
	again, err := Load(path)
	if err != nil {
		t.Fatalf("Load(hashed file): %v", err)
	}
	if len(again) != len(got) || again["amber"] != got["amber"] || again["dawn"] != got["dawn"] {
		t.Errorf("second Load differs: %v vs %v", again, got)
	}
	if fi2, statErr := os.Stat(path); statErr == nil && !fi2.ModTime().Equal(before) {
		t.Error("second Load rewrote an already-hashed file")
	}
}

func TestAgents_SortedNoTokens(t *testing.T) {
	ids := Agents(map[string]string{"zeb": "x", "amber": "y", "dawn": "z"})
	want := []string{"amber", "dawn", "zeb"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("Agents = %v, want %v", ids, want)
	}
}
