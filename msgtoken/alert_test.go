// ClawEh
// License: MIT

package msgtoken

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

// TestManager_UnreadableStoreAlerts: a corrupt per-agent store alerts high
// under the agent's msgtoken id and the manager starts fresh.
func TestManager_UnreadableStoreAlerts(t *testing.T) {
	rec := testalerts.Install(t)
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager("alice", path, 60, 5); err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	got := rec.Alerts()
	if len(got) != 1 || got[0].High || got[0].EventID != "msgtoken:alice" ||
		got[0].Title != "Message-token store unreadable" {
		t.Fatalf("got %+v", got)
	}
}

// TestNamedStore_DeleteNotPersistedAlerts: a revocation that cannot reach
// disk alerts high, because the token would come back after a restart.
func TestNamedStore_DeleteNotPersistedAlerts(t *testing.T) {
	rec := testalerts.Install(t)
	dir := t.TempDir()
	s, err := NewNamedStore(filepath.Join(dir, "named.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.Create("alice", "phone")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Alerts()) != 0 {
		t.Fatalf("healthy store must not alert: %+v", rec.Alerts())
	}
	s.path = filepath.Join(dir, "named.json", "blocked.json") // parent is a file
	if !s.Delete("alice", tok.ID) {
		t.Fatal("Delete must still remove the token in memory")
	}
	got := rec.Alerts()
	if len(got) != 1 || got[0].High || got[0].EventID != "msgtoken:alice" ||
		got[0].Title != "Message-token store not written" {
		t.Fatalf("got %+v", got)
	}
}
