package api

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLaunchCommandHasNoLauncherFlags(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	exePath, args, err := h.resolveLaunchCommand()
	if err != nil {
		t.Fatalf("resolveLaunchCommand() error = %v", err)
	}
	if exePath == "" {
		t.Fatal("resolveLaunchCommand() returned empty executable path")
	}
	// The merged claw binary takes no autostart-time arguments — gateway is
	// the default subcommand and no -no-browser flag is needed.
	if len(args) != 0 {
		t.Fatalf("args = %v, want []", args)
	}
}

func TestBuildDarwinPlistIncludesRunAtLoad(t *testing.T) {
	plist := buildDarwinPlist("/usr/local/bin/claw", []string{})
	if !strings.Contains(plist, "<key>RunAtLoad</key>") {
		t.Fatalf("plist missing RunAtLoad key:\n%s", plist)
	}
	if !strings.Contains(plist, "<true/>") {
		t.Fatalf("plist missing RunAtLoad true value:\n%s", plist)
	}
}
