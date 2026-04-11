package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Tests for internal skills_install.go functions.

func TestWriteOriginMeta_Success(t *testing.T) {
	dir := t.TempDir()

	err := writeOriginMeta(dir, "default", "my-skill", "1.2.3")
	if err != nil {
		t.Fatalf("writeOriginMeta() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".skill-origin.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var meta originMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if meta.Registry != "default" {
		t.Errorf("Registry = %q, want default", meta.Registry)
	}
	if meta.Slug != "my-skill" {
		t.Errorf("Slug = %q, want my-skill", meta.Slug)
	}
	if meta.InstalledVersion != "1.2.3" {
		t.Errorf("InstalledVersion = %q, want 1.2.3", meta.InstalledVersion)
	}
	if meta.Version != 1 {
		t.Errorf("Version = %d, want 1", meta.Version)
	}
	if meta.InstalledAt == 0 {
		t.Error("InstalledAt should not be zero")
	}
}

func TestWriteOriginMeta_NonExistentDir(t *testing.T) {
	// Writing to a non-existent directory should fail.
	err := writeOriginMeta("/nonexistent/path/xyz", "default", "skill", "1.0.0")
	if err == nil {
		t.Error("writeOriginMeta() should error for non-existent directory")
	}
}
