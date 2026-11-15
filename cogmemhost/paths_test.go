// ClawEh - Cognitive Memory
// License: MIT

package cogmemhost

import (
	"context"
	"os"
	"testing"

	"github.com/PivotLLM/cogmem/store"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/utils"
)

// TestMigrate_UpgradesSchemaInPlace: an existing memory is opened once so its
// schema is current before the first turn; nothing else is touched.
func TestMigrate_UpgradesSchemaInPlace(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(Dir(ws), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(store.DBPath(Dir(ws)))
	if err != nil {
		t.Fatal(err)
	}
	if _, domErr := s.CreateDomain(context.Background(), s.DB(), store.CreateDomainParams{
		Name: "Probe", Status: store.StatusActive,
	}); domErr != nil {
		t.Fatal(domErr)
	}
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	Migrate("alice", ws, config.Modes{Dir: 0o700, File: 0o600})

	again, err := store.Open(store.DBPath(Dir(ws)))
	if err != nil {
		t.Fatal(err)
	}
	defer utils.CloseQuietly(again)
	if _, err := again.DomainByName(context.Background(), again.DB(), "Probe"); err != nil {
		t.Fatalf("store lost its data: %v", err)
	}
}

// TestMigrate_FromSchemaV8 opens a memory in the format cogmem v0.0.9 wrote
// (schema 8: no auto_keywords or blocked_keywords columns) through the host's
// opener. The store migrates, the pre-migration snapshot and the database get
// exactly the agents-area file mode, and the agent's keywords and memories are
// kept.
func TestMigrate_FromSchemaV8(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	dir := Dir(ws)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := store.DBPath(dir)

	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateDomain(ctx, s.DB(), store.CreateDomainParams{
		Name: "Invoices", Status: store.StatusActive, KeywordTriggers: "monthly invoice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, addErr := s.AddMemory(ctx, s.DB(), store.AddMemoryParams{
		DomainID: d.ID, Type: store.TypeFact, Text: "due on the first",
		Status: store.StatusActive, Confidence: 0.9,
	}); addErr != nil {
		t.Fatal(addErr)
	}
	// Turn it back into a schema-8 database: the two v9 columns go and the
	// recorded version is 8.
	for _, q := range []string{
		`ALTER TABLE domains DROP COLUMN auto_keywords`,
		`ALTER TABLE domains DROP COLUMN blocked_keywords`,
		`DELETE FROM schema_migrations WHERE version > 8`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(8, 0)`,
		`PRAGMA wal_checkpoint(TRUNCATE)`,
	} {
		if _, execErr := s.DB().ExecContext(ctx, q); execErr != nil {
			t.Fatalf("%s: %v", q, execErr)
		}
	}
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	// A mode the opener must correct, not one it happens to match.
	if chmodErr := os.Chmod(path, 0o644); chmodErr != nil {
		t.Fatal(chmodErr)
	}

	const fileMode = 0o640
	Migrate("alice", ws, config.Modes{Dir: 0o750, File: fileMode})

	snapshot := path + ".pre-v8.db"
	for _, p := range []string{path, snapshot} {
		info, statErr := os.Stat(p)
		if statErr != nil {
			t.Fatalf("stat %s: %v", p, statErr)
		}
		if got := info.Mode().Perm(); got != fileMode {
			t.Errorf("%s mode = %o, want %o", p, got, fileMode)
		}
	}

	again, err := store.Open(path, StoreOptions(config.Modes{Dir: 0o750, File: fileMode})...)
	if err != nil {
		t.Fatal(err)
	}
	defer utils.CloseQuietly(again)
	got, err := again.DomainByName(ctx, again.DB(), "Invoices")
	if err != nil {
		t.Fatalf("domain lost: %v", err)
	}
	if got.KeywordTriggers != "monthly invoice" || got.AutoKeywords != "" || got.BlockedKeywords != "" {
		t.Errorf("keywords after migration = %q / %q / %q, want the agent's kept and the new lists empty",
			got.KeywordTriggers, got.AutoKeywords, got.BlockedKeywords)
	}
	mems, err := again.ListMemories(ctx, again.DB(), got.ID, store.StatusActive)
	if err != nil || len(mems) != 1 || mems[0].Text != "due on the first" {
		t.Errorf("memories after migration = %+v (%v), want the one written before", mems, err)
	}
}

// TestMigrate_NoStoreIsNoOp: a fresh agent gets no directory until memory is
// first written.
func TestMigrate_NoStoreIsNoOp(t *testing.T) {
	ws := t.TempDir()
	Migrate("alice", ws, config.Modes{Dir: 0o700, File: 0o600})
	if _, err := os.Stat(Dir(ws)); err == nil {
		t.Fatal("Migrate created a memory directory for an agent with no memory")
	}
}

func TestDirs(t *testing.T) {
	if Dir("") != "" {
		t.Fatal("empty workspace must give an empty dir, so tools refuse rather than write somewhere")
	}
	if got := Dir("/w"); got != "/w/cogmem" {
		t.Fatalf("Dir = %q", got)
	}
}
