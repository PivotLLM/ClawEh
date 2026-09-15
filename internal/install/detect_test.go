package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSystemdUnit(t *testing.T) {
	dir := t.TempDir()
	unitFile := filepath.Join(dir, "claw.service")
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
	if err := os.WriteFile(unitFile, []byte(unitContent), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := parseSystemdUnit(unitFile, "systemd-system")
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

func TestParseLaunchdPlist(t *testing.T) {
	dir := t.TempDir()
	plistFile := filepath.Join(dir, "com.pivotllm.claweh.plist")
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
	if err := os.WriteFile(plistFile, []byte(plistContent), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := parseLaunchdPlist(plistFile, "launchd-daemon")
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
