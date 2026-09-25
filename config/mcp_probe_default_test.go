// ClawEh
// License: MIT

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMCPLivenessProbeDefault: the probe is on by default, a config that omits
// the key gets the default, and an explicit 0 both loads as 0 and survives a
// save (the field must not be omitempty).
func TestMCPLivenessProbeDefault(t *testing.T) {
	if got := DefaultConfig().Tools.MCP.LivenessProbeSeconds; got != DefaultMCPLivenessProbeSeconds {
		t.Fatalf("default = %d, want %d", got, DefaultMCPLivenessProbeSeconds)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Tools.MCP.LivenessProbeSeconds != DefaultMCPLivenessProbeSeconds {
		t.Errorf("missing key: probe = %d, want the default", cfg.Tools.MCP.LivenessProbeSeconds)
	}

	if werr := os.WriteFile(path, []byte(`{"tools":{"mcp":{"liveness_probe_seconds":0}}}`), 0o600); werr != nil {
		t.Fatal(werr)
	}
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Tools.MCP.LivenessProbeSeconds != 0 {
		t.Errorf("explicit 0: probe = %d, want 0", cfg.Tools.MCP.LivenessProbeSeconds)
	}
	if werr := writeConfig(path, cfg); werr != nil {
		t.Fatalf("writeConfig: %v", werr)
	}
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig after save: %v", err)
	}
	if cfg.Tools.MCP.LivenessProbeSeconds != 0 {
		t.Errorf("explicit 0 after save: probe = %d, want 0 (field must not be omitempty)", cfg.Tools.MCP.LivenessProbeSeconds)
	}
}
