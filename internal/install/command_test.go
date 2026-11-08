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
	alice := func(root bool) *TargetUser { return &TargetUser{HomeDir: "/home/alice", IsRoot: root} }
	cases := []struct {
		name     string
		files    []string // regular files to create first
		dirs     []string // folders to create first
		tu       *TargetUser
		custom   string
		existing *ExistingInstall
		want     string
	}{
		{name: "custom dir", tu: alice(false), custom: "/srv/tools", want: "/srv/tools"},
		{
			name: "existing binary dir kept", dirs: []string{"/usr/local/sbin"}, tu: alice(true),
			existing: &ExistingInstall{BinaryPath: "/usr/local/sbin/claw"}, want: "/usr/local/sbin",
		},
		{
			name: "opt claw with bin binary", files: []string{"/opt/claw/bin/claw"}, tu: alice(true),
			existing: &ExistingInstall{ClawHome: "/opt/claw", BinaryPath: "/opt/claw/bin/claw"}, want: "/opt/claw/bin",
		},
		{
			name: "opt claw with old binary moves to bin", files: []string{"/opt/claw/claw"}, tu: alice(true),
			existing: &ExistingInstall{ClawHome: "/opt/claw", BinaryPath: "/opt/claw/claw"}, want: "/opt/claw/bin",
		},
		{
			name: "opt claw unit naming the old binary", tu: alice(true),
			existing: &ExistingInstall{BinaryPath: "/opt/claw/claw"}, want: "/opt/claw/bin",
		},
		{
			name: "opt claw without a binary", dirs: []string{"/opt/claw"}, tu: alice(true),
			existing: &ExistingInstall{ClawHome: "/opt/claw"}, want: "/opt/claw/bin",
		},
		{name: "system mode", tu: alice(true), want: "/usr/local/bin"},
		{name: "user mode without ~/bin", tu: alice(false), want: "/home/alice/.local/bin"},
		{name: "user mode with ~/bin", dirs: []string{"/home/alice/bin"}, tu: alice(false), want: "/home/alice/bin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := fakeHost(t)
			for _, d := range tc.dirs {
				if err := os.MkdirAll(h.path(d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range tc.files {
				writeHostFile(t, h, f, "x", 0o700)
			}
			got, err := h.resolveBinDir(tc.tu, tc.custom, tc.existing)
			if err != nil || got != tc.want {
				t.Fatalf("resolveBinDir() = %q, %v, want %q", got, err, tc.want)
			}
			if !h.dirExists(got) {
				t.Errorf("%s was not created", got)
			}
		})
	}
}

// An /opt/claw install puts the binary and the openclaw alias in /opt/claw/bin,
// and points at an older binary and alias in /opt/claw without removing them.
func TestOptClawInstall_AliasInBinAndLeftovers(t *testing.T) {
	h := fakeHost(t)
	writeHostFile(t, h, "/opt/claw/claw", "x", 0o700)
	if err := os.Symlink("claw", h.path("/opt/claw/openclaw")); err != nil {
		t.Fatal(err)
	}
	existing := &ExistingInstall{ClawHome: "/opt/claw", BinaryPath: "/opt/claw/claw"}

	binDir, err := h.resolveBinDir(&TargetUser{HomeDir: "/home/alice", IsRoot: true}, "", existing)
	if err != nil || binDir != "/opt/claw/bin" {
		t.Fatalf("resolveBinDir() = %q, %v, want /opt/claw/bin", binDir, err)
	}
	if info, err := os.Stat(h.path(binDir)); err != nil || info.Mode().Perm() != config.DefaultDirMode {
		t.Errorf("/opt/claw/bin mode = %v, %v, want %v", info.Mode().Perm(), err, config.DefaultDirMode)
	}
	writeHostFile(t, h, filepath.Join(binDir, serviceName), "x", 0o700)
	if err := linkOpenClawAlias(h.path(binDir), serviceName); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(h.path("/opt/claw/bin/openclaw")); err != nil || target != "claw" {
		t.Errorf("/opt/claw/bin/openclaw -> %q, %v, want claw", target, err)
	}
	if unit := buildSystemUnit("alice", "alice", filepath.Join(binDir, serviceName), "/home/alice", binDir, "/opt/claw"); !strings.Contains(unit, "ExecStart=/opt/claw/bin/claw") {
		t.Errorf("unit does not start /opt/claw/bin/claw:\n%s", unit)
	}

	leftovers := h.leftoverOptClawFiles(binDir)
	if strings.Join(leftovers, " ") != "/opt/claw/claw /opt/claw/openclaw" {
		t.Errorf("leftoverOptClawFiles() = %q, want the old binary and alias", leftovers)
	}
	for _, p := range leftovers {
		if !h.exists(p) {
			t.Errorf("%s was removed, want it left in place", p)
		}
	}
	if got := h.leftoverOptClawFiles("/usr/local/bin"); got != nil {
		t.Errorf("leftoverOptClawFiles(/usr/local/bin) = %q, want none", got)
	}
}

func TestResolveClawHome(t *testing.T) {
	tempHome := t.TempDir()
	tu := &TargetUser{HomeDir: tempHome}

	// 1. Explicit CLAW_HOME in env
	t.Setenv(global.EnvVarHome, "/custom/claw/home")
	got := fakeHost(t).resolveClawHome(tu, "/usr/local/bin", nil)
	if got != "/custom/claw/home" {
		t.Errorf("resolveClawHome with env = %q, want /custom/claw/home", got)
	}
	t.Setenv(global.EnvVarHome, "")

	// 2. Existing install with ClawHome
	existing := &ExistingInstall{ClawHome: "/opt/claw"}
	got = fakeHost(t).resolveClawHome(tu, "/usr/local/bin", existing)
	if got != "/opt/claw" {
		t.Errorf("resolveClawHome with existing.ClawHome = %q, want /opt/claw", got)
	}

	// 3. Existing install with BinaryPath in /opt/claw
	existing = &ExistingInstall{BinaryPath: "/opt/claw/claw"}
	got = fakeHost(t).resolveClawHome(tu, "/usr/local/bin", existing)
	if got != "/opt/claw" {
		t.Errorf("resolveClawHome with existing.BinaryPath in /opt/claw = %q, want /opt/claw", got)
	}

	// 4. binDir == /opt/claw/bin
	got = fakeHost(t).resolveClawHome(tu, "/opt/claw/bin", nil)
	if got != "/opt/claw" {
		t.Errorf("resolveClawHome with binDir = /opt/claw/bin = %q, want /opt/claw", got)
	}
}
