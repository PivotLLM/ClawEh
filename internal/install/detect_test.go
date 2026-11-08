package install

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSystemdUnit(t *testing.T) {
	h := fakeHost(t)
	unitFile := "/etc/systemd/system/claw.service"
	unitContent := `[Unit]
Description=ClawEh — Personal AI Assistant
After=network-online.target

[Service]
Type=simple
User=clawuser
ExecStart=/opt/claw/claw --some-flag
Environment=CLAW_HOME=/opt/claw
Restart=on-failure

[Install]
WantedBy=multi-user.target
`
	writeHostFile(t, h, unitFile, unitContent, 0o644)

	inst := h.parseSystemdUnit(unitFile, "systemd-system")
	if inst.BinaryPath != "/opt/claw/claw" {
		t.Errorf("BinaryPath = %q, want /opt/claw/claw", inst.BinaryPath)
	}
	if inst.User != "clawuser" {
		t.Errorf("User = %q, want clawuser", inst.User)
	}
	if inst.ClawHome != "/opt/claw" {
		t.Errorf("ClawHome = %q, want /opt/claw", inst.ClawHome)
	}
	if inst.ServiceType != "systemd-system" {
		t.Errorf("ServiceType = %q, want systemd-system", inst.ServiceType)
	}
}

func TestParseSystemdUnit_QuotedAndInferred(t *testing.T) {
	h := fakeHost(t)

	// 1. Quoted Environment="CLAW_HOME=/opt/claw"
	unit1 := "/units/quoted.service"
	content1 := `[Unit]
Description=ClawEh

[Service]
ExecStart=/opt/claw/claw
Environment="CLAW_HOME=/opt/claw"
`
	writeHostFile(t, h, unit1, content1, 0o644)
	inst1 := h.parseSystemdUnit(unit1, "systemd-system")
	if inst1.ClawHome != "/opt/claw" {
		t.Errorf("quoted CLAW_HOME: got %q, want /opt/claw", inst1.ClawHome)
	}

	// 2. No CLAW_HOME env var, but ExecStart=/opt/claw/claw
	unit2 := "/units/inferred.service"
	content2 := `[Unit]
Description=ClawEh

[Service]
ExecStart=/opt/claw/claw
Environment=PATH=/opt/claw:/usr/bin
`
	writeHostFile(t, h, unit2, content2, 0o644)
	inst2 := h.parseSystemdUnit(unit2, "systemd-system")
	if inst2.ClawHome != "/opt/claw" {
		t.Errorf("inferred CLAW_HOME: got %q, want /opt/claw", inst2.ClawHome)
	}
}

func TestParseLaunchdPlist(t *testing.T) {
	h := fakeHost(t)
	plistFile := systemLaunchdPath
	plistContent := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.pivotllm.claweh</string>
	<key>UserName</key>
	<string>macclaw</string>
	<key>ProgramArguments</key>
	<array>
		<string>/opt/claw/bin/claw</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>CLAW_HOME</key>
		<string>/opt/claw</string>
	</dict>
</dict>
</plist>
`
	writeHostFile(t, h, plistFile, plistContent, 0o644)

	inst := h.parseLaunchdPlist(plistFile, "launchd-daemon")
	if inst.BinaryPath != "/opt/claw/bin/claw" {
		t.Errorf("BinaryPath = %q, want /opt/claw/bin/claw", inst.BinaryPath)
	}
	if inst.User != "macclaw" {
		t.Errorf("User = %q, want macclaw", inst.User)
	}
	if inst.ClawHome != "/opt/claw" {
		t.Errorf("ClawHome = %q, want /opt/claw", inst.ClawHome)
	}
}

func TestIsBuildOrTempPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/home/user/claw/build/claw", true},
		{"/tmp/claw", true},
		{"/tmp/claw-upgrade-123/claw", true},
		{"/var/tmp/claw", true},
		{"/opt/claw/claw", false},
		{"/opt/claw", false},
		{"/usr/local/bin/claw", false},
		{"/home/user/.local/bin/claw", false},
		{"/home/user/bin/claw", false},
	}

	for _, tc := range cases {
		got := isBuildOrTempPath(tc.path)
		if got != tc.want {
			t.Errorf("isBuildOrTempPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// fakeHost is a host rooted in a temp dir whose commands all fail, so a test
// never reads the real /opt/claw, /etc/systemd or home directory, nor finds
// the test binary or a claw on PATH.
func fakeHost(t *testing.T) *host {
	t.Helper()
	return &host{
		root:       t.TempDir(),
		executable: func() (string, error) { return "", errors.New("no executable in tests") },
		lookPath:   func(string) (string, error) { return "", exec.ErrNotFound },
		commandOK:  func(string, ...string) bool { return false },
	}
}

// writeHostFile writes content at the host path p, creating its folders.
func writeHostFile(t *testing.T, h *host, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(h.path(p)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.path(p), []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// currentOwner is the name ownerName reports for files the test creates: the
// test's account, or "" when the tests run as root.
func currentOwner(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if u.Username == "root" {
		return ""
	}
	return u.Username
}

// An /opt/claw install is found by its data directory; its binary is
// /opt/claw/bin/claw, else the older /opt/claw/claw, and the folder's owner is
// the account the service runs as.
func TestDetectLinuxInstall_OptClaw(t *testing.T) {
	cases := []struct {
		name    string
		files   []string
		wantBin string
	}{
		{"bin only", []string{"/opt/claw/bin/claw"}, "/opt/claw/bin/claw"},
		{"old location only", []string{"/opt/claw/claw"}, "/opt/claw/claw"},
		{"both", []string{"/opt/claw/claw", "/opt/claw/bin/claw"}, "/opt/claw/bin/claw"},
		{"no binary", []string{"/opt/claw/config.json"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := fakeHost(t)
			for _, f := range tc.files {
				writeHostFile(t, h, f, "x", 0o700)
			}
			inst := h.detectLinuxInstall("/home/alice")
			if inst == nil {
				t.Fatal("detectLinuxInstall() = nil, want the /opt/claw install")
			}
			if inst.ClawHome != "/opt/claw" {
				t.Errorf("ClawHome = %q, want /opt/claw", inst.ClawHome)
			}
			if inst.BinaryPath != tc.wantBin {
				t.Errorf("BinaryPath = %q, want %q", inst.BinaryPath, tc.wantBin)
			}
			if want := currentOwner(t); inst.User != want {
				t.Errorf("User = %q, want %q (the owner of /opt/claw)", inst.User, want)
			}
			if inst.ServicePath != "" {
				t.Errorf("ServicePath = %q, want none", inst.ServicePath)
			}
		})
	}
}

// A system unit wins over everything else and gives the binary, account and
// data directory; whether it is active comes from systemctl.
func TestDetectLinuxInstall_SystemUnit(t *testing.T) {
	h := fakeHost(t)
	writeHostFile(t, h, systemUnitPath, "[Service]\nUser=alice\nExecStart=/opt/claw/bin/claw\n", 0o644)
	writeHostFile(t, h, "/opt/claw/claw", "x", 0o700)
	var asked []string
	h.commandOK = func(name string, args ...string) bool {
		asked = append([]string{name}, args...)
		return true
	}

	inst := h.detectLinuxInstall("/home/alice")
	if inst == nil {
		t.Fatal("detectLinuxInstall() = nil, want the system service")
	}
	if inst.ServiceType != "systemd-system" || inst.ServicePath != systemUnitPath {
		t.Errorf("service = %q at %q, want systemd-system at %s", inst.ServiceType, inst.ServicePath, systemUnitPath)
	}
	if inst.BinaryPath != "/opt/claw/bin/claw" {
		t.Errorf("BinaryPath = %q, want the unit's ExecStart /opt/claw/bin/claw", inst.BinaryPath)
	}
	if inst.User != "alice" || inst.ClawHome != "/opt/claw" {
		t.Errorf("User, ClawHome = %q, %q, want alice, /opt/claw", inst.User, inst.ClawHome)
	}
	if !inst.IsActive || strings.Join(asked, " ") != "systemctl is-active --quiet claw" {
		t.Errorf("IsActive = %v from %q, want true from systemctl is-active", inst.IsActive, asked)
	}
}

// A user unit is found under the account's home; with no CLAW_HOME in it the
// data directory is inferred from the binary.
func TestDetectLinuxInstall_UserUnit(t *testing.T) {
	h := fakeHost(t)
	writeHostFile(t, h, userUnitPath("/home/alice"), "[Service]\nExecStart=/opt/claw/bin/claw\n", 0o644)

	inst := h.detectLinuxInstall("/home/alice")
	if inst == nil || inst.ServiceType != "systemd-user" {
		t.Fatalf("detectLinuxInstall() = %+v, want the user service", inst)
	}
	if inst.BinaryPath != "/opt/claw/bin/claw" || inst.ClawHome != "/opt/claw" {
		t.Errorf("BinaryPath, ClawHome = %q, %q, want /opt/claw/bin/claw, /opt/claw", inst.BinaryPath, inst.ClawHome)
	}
	if inst.IsActive {
		t.Error("IsActive = true, want false (systemctl reports it inactive)")
	}
}

// With no service, no /opt/claw and no installed binary, nothing is found.
func TestDetectLinuxInstall_Nothing(t *testing.T) {
	if inst := fakeHost(t).detectLinuxInstall("/home/alice"); inst != nil {
		t.Errorf("detectLinuxInstall() = %+v, want nil", inst)
	}
}
