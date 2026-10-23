// ClawEh
// License: MIT

package config

import (
	"os"
	"path/filepath"
	"slices"
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

// A config written before session modes were removed must still load: every
// agent now runs one conversation, so session.mode and session.identity_links
// are unknown keys (reported by the load warning) and session.retention_days
// beside them is read as before.
func TestLoadConfig_IgnoresRemovedSessionMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	body := `{"session":{"mode":"per-user","identity_links":{"alice":["telegram:1"]},"retention_days":9}}`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if got := cfg.Session.RetentionDays; got != 9 {
		t.Fatalf("retention_days = %d, want 9 (the removed keys must not disturb their neighbours)", got)
	}
	doc, err := decodeDocument([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"session.identity_links", "session.mode"}
	if got := unknownConfigKeys(doc); !slices.Equal(got, want) {
		t.Fatalf("unknownConfigKeys = %v, want %v", got, want)
	}
}
