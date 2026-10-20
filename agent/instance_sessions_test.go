// ClawEh
// License: MIT

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// A sessions directory that still holds a JSONL-layout session must stop the
// store from opening, or the first turn would mint a fresh window and the
// migration would later skip the session with its history stranded.
func TestInitSessionStore_RefusesUnmigratedSessions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.meta.json"), []byte(`{"key":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := initSessionStore(dir, config.Modes{Dir: 0o700, File: 0o600})
	if err == nil {
		closeT(t, store)
		t.Fatal("expected an error for an unmigrated sessions directory")
	}
	if !strings.Contains(err.Error(), "claw sessions migrate") {
		t.Fatalf("error should tell the operator what to run, got: %v", err)
	}
}

// Migrated sources are renamed *.migrated and must not trip the guard.
func TestInitSessionStore_IgnoresMigratedSources(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"x.meta.json.migrated", "x.jsonl.migrated"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	store, err := initSessionStore(dir, config.Modes{Dir: 0o700, File: 0o600})
	if err != nil {
		t.Fatalf("initSessionStore: %v", err)
	}
	closeT(t, store)
}

// A workspace with no sessions directory yet is the normal first start.
func TestInitSessionStore_MissingDirIsFine(t *testing.T) {
	store, err := initSessionStore(filepath.Join(t.TempDir(), "sessions"), config.Modes{Dir: 0o700, File: 0o600})
	if err != nil {
		t.Fatalf("initSessionStore: %v", err)
	}
	closeT(t, store)
}
