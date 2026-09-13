// ClawEh
// License: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A config written before memory.retention.protect_unconsolidated was removed
// must still load: the key is unknown now and is ignored, and the retention
// windows beside it are read as before.
func TestLoadConfig_IgnoresRemovedProtectUnconsolidated(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	body := `{"agents":{"defaults":{"memory":{"retention":{"protect_unconsolidated":true,"event_days":12}}}}}`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if got := cfg.Agents.Defaults.Memory.Retention.EventDays; got != 12 {
		t.Fatalf("event_days = %d, want 12 (the removed key must not disturb its neighbours)", got)
	}
}
