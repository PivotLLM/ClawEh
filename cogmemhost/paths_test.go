// ClawEh - Cognitive Memory
// License: MIT

package cogmemhost

import (
	"context"
	"os"
	"testing"

	"github.com/PivotLLM/cogmem/store"
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
	_ = s.Close()

	Migrate("alice", ws)

	again, err := store.Open(store.DBPath(Dir(ws)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	if _, err := again.DomainByName(context.Background(), again.DB(), "Probe"); err != nil {
		t.Fatalf("store lost its data: %v", err)
	}
}

// TestMigrate_NoStoreIsNoOp: a fresh agent gets no directory until memory is
// first written.
func TestMigrate_NoStoreIsNoOp(t *testing.T) {
	ws := t.TempDir()
	Migrate("alice", ws)
	if _, err := os.Stat(Dir(ws)); err == nil {
		t.Fatal("Migrate created a memory directory for an agent with no memory")
	}
}

func TestDirs(t *testing.T) {
	if Dir("") != "" {
		t.Fatal("empty workspace must give an empty dir, so tools refuse rather than write somewhere")
	}
	if got := SubagentDir("/w", "agent:alice:subagent:abc"); got != "/w/cogmem/subagents/agent_alice_subagent_abc" {
		t.Fatalf("SubagentDir = %q", got)
	}
}
