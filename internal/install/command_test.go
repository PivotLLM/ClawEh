package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal"
)

// An existing service decides the mode: re-running the installer at the wrong
// privilege level is refused instead of writing a second unit of the other
// kind for the same install.
func TestCheckExistingMode(t *testing.T) {
	system := &ExistingInstall{ServiceType: "systemd-system", ServicePath: "/etc/systemd/system/claw.service", User: "ai"}
	userSvc := &ExistingInstall{ServiceType: "systemd-user", ServicePath: "/home/ai/.config/systemd/user/claw.service", User: "ai"}
	daemon := &ExistingInstall{ServiceType: "launchd-daemon", ServicePath: "/Library/LaunchDaemons/com.pivotllm.claweh.plist"}
	agent := &ExistingInstall{ServiceType: "launchd-agent", ServicePath: "/Users/ai/Library/LaunchAgents/com.pivotllm.claweh.plist", User: "ai"}
	binaryOnly := &ExistingInstall{BinaryPath: "/opt/claw/claw"}

	cases := []struct {
		name     string
		existing *ExistingInstall
		isRoot   bool
		wantErr  string
	}{
		{"system service without sudo", system, false, "run `sudo claw install`"},
		{"system service with sudo", system, true, ""},
		{"user service with sudo", userSvc, true, "as ai, without sudo"},
		{"user service without sudo", userSvc, false, ""},
		{"launchd daemon without sudo", daemon, false, "run `sudo claw install`"},
		{"launchd agent with sudo", agent, true, "without sudo"},
		{"binary only, either way", binaryOnly, false, ""},
		{"nothing installed", nil, true, ""},
	}
	for _, c := range cases {
		err := checkExistingMode(c.existing, c.isRoot)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.wantErr)
		}
	}
}

func TestBuildUnit_RunsAsUserAndStartsAtBoot(t *testing.T) {
	t.Setenv(global.EnvVarHome, "") // defaults to user's .claw
	t.Setenv("PATH", "/home/alice/.local/bin:/home/alice/.nvm/versions/node/v24/bin:/usr/bin")
	unit := buildUnit("alice", "alice", "/home/alice/bin/claw", "/home/alice/bin")

	wants := []string{
		"User=alice",
		"Group=alice",
		"ExecStart=/home/alice/bin/claw",
		"WantedBy=multi-user.target",
		"KillMode=mixed",
		"TimeoutStopSec=60",
		"NoNewPrivileges=yes",
		"PrivateTmp=yes",
		"Environment=CLAW_HOME=/home/alice/.claw",
		"Environment=PATH=/home/alice/bin:/home/alice/.local/bin:/home/alice/.nvm/versions/node/v24/bin:/usr/local/bin:/usr/bin:/bin",
	}
	for _, w := range wants {
		if !strings.Contains(unit, w) {
			t.Errorf("unit missing %q:\n%s", w, unit)
		}
	}
}

func TestBuildUnit_IncludesClawHomeWhenSet(t *testing.T) {
	t.Setenv(global.EnvVarHome, "/srv/claw-data")
	unit := buildUnit("bob", "staff", "/home/bob/.local/bin/claw", "/home/bob/.local/bin")

	if !strings.Contains(unit, "Environment="+global.EnvVarHome+"=/srv/claw-data") {
		t.Errorf("unit should carry CLAW_HOME when set:\n%s", unit)
	}
}

func TestServicePATH_PrependsBinDirAndDedups(t *testing.T) {
	t.Setenv("PATH", "/home/bob/bin:/usr/bin:/home/bob/.local/bin")
	got := servicePATH("/home/bob", "/home/bob/bin")
	want := "/home/bob/bin:/home/bob/.local/bin:/usr/local/bin:/usr/bin:/bin"
	if got != want {
		t.Errorf("servicePATH = %q, want %q", got, want)
	}
}

func TestApplyServerSettings_WritesHostAndPort(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(global.EnvVarHome, dir) // CLAW_HOME → config under dir

	if err := applyServerSettings("0.0.0.0", 12345); err != nil {
		t.Fatalf("applyServerSettings: %v", err)
	}
	cfg, err := config.LoadConfig(internal.GetConfigPath())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Gateway.Host != "0.0.0.0" || cfg.Gateway.Port != 12345 {
		t.Fatalf("got %s:%d, want 0.0.0.0:12345", cfg.Gateway.Host, cfg.Gateway.Port)
	}

	// A blank host / zero port must leave the stored values untouched.
	if err := applyServerSettings("", 0); err != nil {
		t.Fatalf("applyServerSettings (no-op): %v", err)
	}
	cfg, loadErr := config.LoadConfig(internal.GetConfigPath())
	if loadErr != nil {
		t.Fatalf("LoadConfig: %v", loadErr)
	}
	if cfg.Gateway.Host != "0.0.0.0" || cfg.Gateway.Port != 12345 {
		t.Fatalf("blank args changed bind: got %s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
	}
}

func TestApplyAllowlist_WritesCIDRs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(global.EnvVarHome, dir)

	if err := applyAllowlist("192.168.5.0/24, 10.1.0.0/16 ,"); err != nil {
		t.Fatalf("applyAllowlist: %v", err)
	}
	cfg, err := config.LoadConfig(internal.GetConfigPath())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Gateway.AllowedCIDRs) != 2 ||
		cfg.Gateway.AllowedCIDRs[0] != "192.168.5.0/24" || cfg.Gateway.AllowedCIDRs[1] != "10.1.0.0/16" {
		t.Fatalf("Gateway.AllowedCIDRs = %v, want [192.168.5.0/24 10.1.0.0/16]", cfg.Gateway.AllowedCIDRs)
	}
}

func TestPrintListenerSummary(t *testing.T) {
	var b strings.Builder
	printListenerSummary(&b, config.GatewayConfig{Host: "127.0.0.1", Port: 18790})
	got := b.String()
	for _, want := range []string{
		"WebUI on this machine:  http://127.0.0.1:18790/",
		"HTTPS on the network:   https://",
		"Allowed networks:       localhost only",
		"Certificate:            self-signed",
		"Network and certificate settings can be changed in the WebUI or config.json.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("default summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "unencrypted") {
		t.Errorf("loopback HTTP reported as network HTTP:\n%s", got)
	}
	// No paragraph: every line is one short labelled fact.
	for l := range strings.SplitSeq(strings.TrimSpace(got), "\n") {
		if len(l) > 100 {
			t.Errorf("summary line too long: %q", l)
		}
	}

	b.Reset()
	printListenerSummary(&b, config.GatewayConfig{Host: "192.168.1.5", Port: 9000, AllowedCIDRs: []string{"192.168.1.0/24"}, TLS: config.TLSConfig{Mode: config.TLSModeOff}})
	got = b.String()
	for _, want := range []string{"WebUI on the network:   http://192.168.1.5:9000/  (HTTP, unencrypted, not recommended)", "HTTPS:                  off", "Allowed networks:       192.168.1.0/24"} {
		if !strings.Contains(got, want) {
			t.Errorf("network HTTP summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Certificate:") {
		t.Errorf("certificate line with HTTPS off:\n%s", got)
	}

	b.Reset()
	printListenerSummary(&b, config.GatewayConfig{TLSPort: 9443, TLS: config.TLSConfig{Mode: config.TLSModeLocalhost, CertFile: "/etc/claw/my.crt"}})
	got = b.String()
	if !strings.Contains(got, "HTTPS on this machine:  https://127.0.0.1:9443/") || !strings.Contains(got, "Certificate:            /etc/claw/my.crt") {
		t.Errorf("localhost HTTPS summary:\n%s", got)
	}
	if strings.Contains(got, "Allowed networks") {
		t.Errorf("allowlist shown with nothing on the network:\n%s", got)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":              "''",
		"/etc/claw":     "'/etc/claw'",
		"it's":          `'it'"'"'s'`,
		"/tmp/a b/claw": "'/tmp/a b/claw'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIsNetworkBind pins which bind addresses count as off-box, since that is
// what gates the install-time refusal.
func TestIsNetworkBind(t *testing.T) {
	for _, h := range []string{"", "127.0.0.1", "localhost", "::1", "[::1]", " 127.0.0.1 "} {
		if isNetworkBind(h) {
			t.Errorf("isNetworkBind(%q) = true, want false", h)
		}
	}
	for _, h := range []string{"0.0.0.0", "::", "192.168.1.10", "10.0.0.5"} {
		if !isNetworkBind(h) {
			t.Errorf("isNetworkBind(%q) = false, want true", h)
		}
	}
}

// TestLinkOpenClawAlias covers the symlink the Rabbit R1 needs: rabbit-agent
// spawns `openclaw acp`, so an install that omits it leaves the R1 unable to
// connect even though everything else works.
func TestLinkOpenClawAlias(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claw"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := linkOpenClawAlias(dir, "claw"); err != nil {
		t.Fatalf("linkOpenClawAlias() error = %v", err)
	}
	link := filepath.Join(dir, "openclaw")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("openclaw is not a symlink: %v", err)
	}
	if target != "claw" {
		t.Fatalf("openclaw -> %q, want %q (relative, so moving the dir keeps it valid)", target, "claw")
	}

	// Re-running install must replace an existing link rather than failing.
	if err := linkOpenClawAlias(dir, "claw"); err != nil {
		t.Fatalf("second linkOpenClawAlias() error = %v, want it to replace the existing link", err)
	}
}

func TestBuildUserUnit(t *testing.T) {
	unit := buildUserUnit("/home/alice/.local/bin/claw", "/home/alice", "/home/alice/.local/bin", "/home/alice/.claw")
	if strings.Contains(unit, "User=") {
		t.Errorf("user unit must NOT contain User= directive:\n%s", unit)
	}
	if strings.Contains(unit, "Group=") {
		t.Errorf("user unit must NOT contain Group= directive:\n%s", unit)
	}
	if !strings.Contains(unit, "WantedBy=default.target") {
		t.Errorf("user unit must specify WantedBy=default.target:\n%s", unit)
	}
	if !strings.Contains(unit, "ExecStart=/home/alice/.local/bin/claw") {
		t.Errorf("user unit missing ExecStart:\n%s", unit)
	}
	if !strings.Contains(unit, "WorkingDirectory=/home/alice") {
		t.Errorf("user unit missing WorkingDirectory:\n%s", unit)
	}
	if !strings.Contains(unit, "TimeoutStopSec=60") {
		t.Errorf("user unit missing TimeoutStopSec=60:\n%s", unit)
	}
	if !strings.Contains(unit, "KillMode=mixed") {
		t.Errorf("user unit missing KillMode=mixed:\n%s", unit)
	}
	if !strings.Contains(unit, "NoNewPrivileges=yes") {
		t.Errorf("user unit missing NoNewPrivileges=yes:\n%s", unit)
	}
	if strings.Contains(unit, "PrivateTmp=") {
		t.Errorf("user unit must not set PrivateTmp= (needs unprivileged user namespaces in a per-user manager):\n%s", unit)
	}
	if !strings.Contains(unit, "Environment=CLAW_HOME=/home/alice/.claw") {
		t.Errorf("user unit missing Environment=CLAW_HOME:\n%s", unit)
	}
}

func TestBuildLaunchdPlist_UserMode(t *testing.T) {
	plist := buildLaunchdPlist("com.pivotllm.claweh", "alice", "staff", "/Users/alice/.local/bin/claw", "/Users/alice", "/Users/alice/.local/bin", "/Users/alice/.claw", false)
	if strings.Contains(plist, "<key>UserName</key>") {
		t.Errorf("LaunchAgent plist in user mode must NOT contain UserName:\n%s", plist)
	}
	if strings.Contains(plist, "<key>GroupName</key>") {
		t.Errorf("LaunchAgent plist in user mode must NOT contain GroupName:\n%s", plist)
	}
	if !strings.Contains(plist, "<string>/Users/alice/.local/bin/claw</string>") {
		t.Errorf("LaunchAgent missing binary path:\n%s", plist)
	}
	if !strings.Contains(plist, "<key>RunAtLoad</key>\n\t<true/>") {
		t.Errorf("LaunchAgent missing RunAtLoad:\n%s", plist)
	}
	if !strings.Contains(plist, "<string>/Users/alice</string>") {
		t.Errorf("LaunchAgent missing WorkingDirectory:\n%s", plist)
	}
	if !strings.Contains(plist, "<string>/Users/alice/.claw</string>") {
		t.Errorf("LaunchAgent missing CLAW_HOME:\n%s", plist)
	}
}

func TestBuildLaunchdPlist_SystemMode(t *testing.T) {
	plist := buildLaunchdPlist("com.pivotllm.claweh", "alice", "staff", "/usr/local/bin/claw", "/Users/alice", "/usr/local/bin", "/opt/claw", true)
	if !strings.Contains(plist, "<key>UserName</key>\n\t<string>alice</string>") {
		t.Errorf("LaunchDaemon plist in system mode missing UserName:\n%s", plist)
	}
	if !strings.Contains(plist, "<key>GroupName</key>\n\t<string>staff</string>") {
		t.Errorf("LaunchDaemon plist in system mode missing GroupName:\n%s", plist)
	}
	if !strings.Contains(plist, "<string>/usr/local/bin/claw</string>") {
		t.Errorf("LaunchDaemon missing binary path:\n%s", plist)
	}
	if !strings.Contains(plist, "<string>/opt/claw</string>") {
		t.Errorf("LaunchDaemon missing CLAW_HOME:\n%s", plist)
	}
}

func TestResolveBinDir(t *testing.T) {
	tempHome := t.TempDir()

	// 1. Explicit custom dir
	customDir := filepath.Join(tempHome, "custom", "bin")
	dir, err := resolveBinDir(&TargetUser{HomeDir: tempHome, IsRoot: false}, customDir, nil)
	if err != nil || dir != customDir {
		t.Fatalf("resolveBinDir custom: got %v, %v, want %s", dir, err, customDir)
	}

	// 2. Existing installation binary directory preservation
	optClawDir := filepath.Join(tempHome, "opt", "claw")
	if mkErr := os.MkdirAll(optClawDir, 0o755); mkErr != nil {
		t.Fatal(mkErr)
	}
	existing := &ExistingInstall{BinaryPath: filepath.Join(optClawDir, "claw")}
	dir, err = resolveBinDir(&TargetUser{HomeDir: tempHome, IsRoot: false}, "", existing)
	if err != nil || dir != optClawDir {
		t.Fatalf("resolveBinDir existing: got %v, want %s", dir, optClawDir)
	}

	// 3. System Mode (IsRoot = true)
	dir, err = resolveBinDir(&TargetUser{HomeDir: tempHome, IsRoot: true}, "", nil)
	if err != nil || dir != "/usr/local/bin" {
		t.Fatalf("resolveBinDir system: got %v, %v, want /usr/local/bin", dir, err)
	}

	// 4. User Mode without ~/bin -> ~/.local/bin
	dir, err = resolveBinDir(&TargetUser{HomeDir: tempHome, IsRoot: false}, "", nil)
	expectedLocal := filepath.Join(tempHome, ".local", "bin")
	if err != nil || dir != expectedLocal {
		t.Fatalf("resolveBinDir user without ~/bin: got %v, want %s", dir, expectedLocal)
	}

	// 5. User Mode with existing ~/bin -> ~/bin
	expectedBin := filepath.Join(tempHome, "bin")
	if mkErr := os.MkdirAll(expectedBin, 0o755); mkErr != nil {
		t.Fatal(mkErr)
	}
	dir, err = resolveBinDir(&TargetUser{HomeDir: tempHome, IsRoot: false}, "", nil)
	if err != nil || dir != expectedBin {
		t.Fatalf("resolveBinDir user with ~/bin: got %v, want %s", dir, expectedBin)
	}
}

func TestResolveClawHome(t *testing.T) {
	tempHome := t.TempDir()
	tu := &TargetUser{HomeDir: tempHome}

	// 1. Explicit CLAW_HOME in env
	t.Setenv(global.EnvVarHome, "/custom/claw/home")
	got := resolveClawHome(tu, "/usr/local/bin", nil)
	if got != "/custom/claw/home" {
		t.Errorf("resolveClawHome with env = %q, want /custom/claw/home", got)
	}
	t.Setenv(global.EnvVarHome, "")

	// 2. Existing install with ClawHome
	existing := &ExistingInstall{ClawHome: "/opt/claw"}
	got = resolveClawHome(tu, "/usr/local/bin", existing)
	if got != "/opt/claw" {
		t.Errorf("resolveClawHome with existing.ClawHome = %q, want /opt/claw", got)
	}

	// 3. Existing install with BinaryPath in /opt/claw
	existing = &ExistingInstall{BinaryPath: "/opt/claw/claw"}
	got = resolveClawHome(tu, "/usr/local/bin", existing)
	if got != "/opt/claw" {
		t.Errorf("resolveClawHome with existing.BinaryPath in /opt/claw = %q, want /opt/claw", got)
	}

	// 4. binDir == /opt/claw
	got = resolveClawHome(tu, "/opt/claw", nil)
	if got != "/opt/claw" {
		t.Errorf("resolveClawHome with binDir = /opt/claw = %q, want /opt/claw", got)
	}
}
