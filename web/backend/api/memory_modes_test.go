package api

import (
	"os"
	"path/filepath"
	"testing"
)

// Without a readable configuration the memory store is not opened at all: an
// open with the default mode would undo the agents mode the gateway set.
func TestOpenMemoryStore_RefusesWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(configPath)
	dbPath := filepath.Join(dir, "cogmem", "cogmem.db")
	s, err := h.openMemoryStore(dbPath)
	if err == nil {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
		t.Fatal("opened the memory store without a configuration")
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Errorf("the database was created or touched: %v", statErr)
	}
}
