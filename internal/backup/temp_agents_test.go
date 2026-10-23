// ClawEh
// License: MIT

package backup

import (
	"path/filepath"
	"testing"
	"time"
)

// Temporary agents (internal/temp/<uuid> and internal/temp_agents.json) are
// never archived, while the rest of internal/ still is.
func TestRunSkipsTemporaryAgents(t *testing.T) {
	src := fixture(t)
	writeFileT(t, filepath.Join(src.Home, "internal", "temp_agents.json"), `{"version":1,"agents":[]}`, 0o600)
	tempDir := filepath.Join(src.Home, "internal", "temp", "0c4db133-cafd-4225-9933-56cc2812ef9d")
	writeFileT(t, filepath.Join(tempDir, "sessions", "s.archive.db"), "not a db", 0o600)
	writeFileT(t, filepath.Join(tempDir, "workspace", "AGENTS.md"), "seed", 0o600)

	res, err := Run(src, filepath.Join(t.TempDir(), "out"), time.Now(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("skipped = %+v: a temporary agent's database was examined", res.Skipped)
	}
	_, entries := readTar(t, res.Archive)
	for name := range entries {
		if isTempAgentPath(name) {
			t.Errorf("%s must not be archived", name)
		}
	}
	if _, ok := entries["internal/tokens.json"]; !ok {
		t.Error("the rest of internal/ must still be archived")
	}
}
