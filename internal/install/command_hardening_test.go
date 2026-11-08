package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/global"
)

// blockPath puts a regular file at the host path p, so creating a folder at
// or under it fails.
func blockPath(t *testing.T, h *host, p string) {
	t.Helper()
	writeHostFile(t, h, p, "not a folder", 0o600)
}

// When a bin folder cannot be created, resolveBinDir says which one.
func TestResolveBinDir_CreationFailures(t *testing.T) {
	alice := func(root bool) *TargetUser { return &TargetUser{HomeDir: "/home/alice", IsRoot: root} }
	cases := []struct {
		name     string
		block    string
		tu       *TargetUser
		custom   string
		existing *ExistingInstall
		wantIn   string
	}{
		{name: "custom", block: "/srv", tu: alice(false), custom: "/srv/tools", wantIn: "/srv/tools"},
		{name: "system", block: "/usr/local", tu: alice(true), wantIn: "/usr/local/bin"},
		{name: "user", block: "/home/alice/.local", tu: alice(false), wantIn: "/home/alice/.local/bin"},
		{
			name: "opt claw from old binary", block: "/opt/claw/bin", tu: alice(true),
			existing: &ExistingInstall{BinaryPath: "/opt/claw/claw"}, wantIn: "/opt/claw/bin",
		},
		{
			name: "opt claw home", block: "/opt/claw/bin", tu: alice(true),
			existing: &ExistingInstall{ClawHome: "/opt/claw"}, wantIn: "/opt/claw/bin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := fakeHost(t)
			blockPath(t, h, tc.block)
			got, err := h.resolveBinDir(tc.tu, tc.custom, tc.existing)
			if err == nil {
				t.Fatalf("resolveBinDir() = %q, nil, want an error", got)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not name %s", err, tc.wantIn)
			}
		})
	}
}

// A detected binary whose folder is gone is not reused: the default for the
// mode applies.
func TestResolveBinDir_ExistingFolderGone(t *testing.T) {
	h := fakeHost(t)
	got, err := h.resolveBinDir(&TargetUser{HomeDir: "/home/alice", IsRoot: true}, "",
		&ExistingInstall{BinaryPath: "/usr/local/sbin/claw"})
	if err != nil || got != "/usr/local/bin" {
		t.Fatalf("resolveBinDir() = %q, %v, want /usr/local/bin", got, err)
	}
}

// A binary in a folder next to /opt/claw is not an /opt/claw install: its
// folder is kept as it is.
func TestResolveBinDir_SiblingOfOptClaw(t *testing.T) {
	h := fakeHost(t)
	if err := os.MkdirAll(h.path("/opt/claw-old"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := h.resolveBinDir(&TargetUser{HomeDir: "/home/alice", IsRoot: true}, "",
		&ExistingInstall{BinaryPath: "/opt/claw-old/claw"})
	if err != nil || got != "/opt/claw-old" {
		t.Fatalf("resolveBinDir() = %q, %v, want /opt/claw-old", got, err)
	}
}

// ensureOptClawBinDir leaves an existing /opt/claw/bin in place.
func TestEnsureOptClawBinDir_Existing(t *testing.T) {
	h := fakeHost(t)
	writeHostFile(t, h, "/opt/claw/bin/claw", "x", 0o700)
	got, err := h.ensureOptClawBinDir()
	if err != nil || got != optClawBinDir {
		t.Fatalf("ensureOptClawBinDir() = %q, %v", got, err)
	}
	if !h.fileExists("/opt/claw/bin/claw") {
		t.Error("the binary in /opt/claw/bin was removed")
	}
}

// resolveClawHome: the env, then the detected install, then /opt/claw when the
// binary goes there or the folder exists, else ~/.claw.
func TestResolveClawHome_Fallbacks(t *testing.T) {
	t.Setenv(global.EnvVarHome, "")
	tu := &TargetUser{HomeDir: "/home/alice"}
	home := filepath.Join("/home/alice", global.DefaultDataDir)

	h := fakeHost(t)
	if got := h.resolveClawHome(tu, "/home/alice/bin", nil); got != home {
		t.Errorf("no install, no /opt/claw = %q, want %q", got, home)
	}
	if got := h.resolveClawHome(tu, "/home/alice/bin", &ExistingInstall{BinaryPath: "/opt/claw-old/claw"}); got != home {
		t.Errorf("binary next to /opt/claw = %q, want %q", got, home)
	}
	if got := h.resolveClawHome(tu, "/home/alice/bin", &ExistingInstall{}); got != home {
		t.Errorf("empty install = %q, want %q", got, home)
	}
	if err := os.MkdirAll(h.path("/opt/claw"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := h.resolveClawHome(tu, "/home/alice/bin", nil); got != "/opt/claw" {
		t.Errorf("/opt/claw present = %q, want /opt/claw", got)
	}
}
