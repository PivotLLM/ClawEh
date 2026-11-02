package internal

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withoutInstallRoot points the system install location at an empty temp
// dir, so a real /opt/claw on the test machine does not take over.
func withoutInstallRoot(t *testing.T) {
	t.Helper()
	saved := installRoot
	installRoot = t.TempDir()
	t.Cleanup(func() { installRoot = saved })
}

func TestGetConfigPath(t *testing.T) {
	withoutInstallRoot(t)
	// Empty, and restored after: GetClawHome may set it.
	t.Setenv("CLAW_HOME", "")
	t.Setenv("HOME", "/tmp/home")

	got := GetConfigPath()
	want := filepath.Join("/tmp/home", ".claw", "config.json")

	assert.Equal(t, want, got)
}

func TestGetConfigPath_WithCLAW_HOME(t *testing.T) {
	withoutInstallRoot(t)
	t.Setenv("CLAW_HOME", "/custom/claw")
	t.Setenv("HOME", "/tmp/home")

	got := GetConfigPath()
	want := filepath.Join("/custom/claw", "config.json")

	assert.Equal(t, want, got)
}

func TestGetConfigPath_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific HOME behavior varies; run on windows")
	}

	testUserProfilePath := `C:\Users\Test`
	t.Setenv("USERPROFILE", testUserProfilePath)

	got := GetConfigPath()
	want := filepath.Join(testUserProfilePath, ".claw", "config.json")

	require.True(t, strings.EqualFold(got, want), "GetConfigPath() = %q, want %q", got, want)
}
