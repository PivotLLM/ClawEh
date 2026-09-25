// ClawEh
// License: MIT

package cogmemhost

import (
	"os"
	"testing"

	"github.com/PivotLLM/cogmem/store"

	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

// TestMigrate_AlertsOnFailure: a memory store that cannot be opened (its
// database path is a directory) raises one high alert keyed by the agent.
func TestMigrate_AlertsOnFailure(t *testing.T) {
	rec := testalerts.Install(t)
	ws := t.TempDir()
	if err := os.MkdirAll(store.DBPath(Dir(ws)), 0o755); err != nil {
		t.Fatal(err)
	}
	Migrate("alice", ws)
	got := rec.Alerts()
	if len(got) != 1 || got[0].High || got[0].EventID != "cogmem:alice" ||
		got[0].Title != "Cognitive memory migration failed" {
		t.Fatalf("got %+v", got)
	}
}
