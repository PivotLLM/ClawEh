package install

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
)

func TestBuildUnit_RunsAsUserAndStartsAtBoot(t *testing.T) {
	t.Setenv(global.EnvVarHome, "") // ensure default data dir → no CLAW_HOME line
	// The unit must bake the user's interactive PATH (so CLI agents in ~/.local/bin
	// and an nvm node bin are reachable) with binDir first and system dirs appended.
	t.Setenv("PATH", "/home/alice/.local/bin:/home/alice/.nvm/versions/node/v24/bin:/usr/bin")
	unit := buildUnit("alice", "alice", "/home/alice/bin/claw", "/home/alice/bin")

	wants := []string{
		"User=alice",
		"Group=alice",
		"ExecStart=/home/alice/bin/claw",
		"WantedBy=multi-user.target",
		"Environment=PATH=/home/alice/bin:/home/alice/.local/bin:/home/alice/.nvm/versions/node/v24/bin:/usr/bin:/usr/local/bin:/bin",
	}
	for _, w := range wants {
		if !strings.Contains(unit, w) {
			t.Errorf("unit missing %q:\n%s", w, unit)
		}
	}
	if strings.Contains(unit, global.EnvVarHome+"=") {
		t.Errorf("unit should omit %s when default data dir is used:\n%s", global.EnvVarHome, unit)
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
	// binDir already present in PATH must not be duplicated; system dirs appended once.
	t.Setenv("PATH", "/home/bob/bin:/usr/bin:/home/bob/.local/bin")
	got := servicePATH("/home/bob/bin")
	want := "/home/bob/bin:/usr/bin:/home/bob/.local/bin:/usr/local/bin:/bin"
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
	cfg, _ = config.LoadConfig(internal.GetConfigPath())
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

func TestAccessURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(global.EnvVarHome, dir)
	write := func(host string, port int) {
		cfg := config.DefaultConfig()
		cfg.Gateway.Host = host
		cfg.Gateway.Port = port
		if err := config.SaveConfig(internal.GetConfigPath(), cfg); err != nil {
			t.Fatalf("SaveConfig: %v", err)
		}
	}

	write("127.0.0.1", 18790)
	if got := accessURL(); got != "http://localhost:18790" {
		t.Errorf("loopback URL = %q, want http://localhost:18790", got)
	}
	write("192.168.1.5", 9000)
	if got := accessURL(); got != "http://192.168.1.5:9000" {
		t.Errorf("specific-host URL = %q, want http://192.168.1.5:9000", got)
	}
	// All-interfaces: resolves to a LAN IP (or the <server-ip> placeholder), but
	// always carries the right scheme and port.
	write("0.0.0.0", 8080)
	if got := accessURL(); !strings.HasPrefix(got, "http://") || !strings.HasSuffix(got, ":8080") {
		t.Errorf("all-interfaces URL = %q, want http://…:8080", got)
	}
}

func TestIsPublicBind(t *testing.T) {
	for _, h := range []string{"", "127.0.0.1", "localhost", "::1"} {
		if isPublicBind(h) {
			t.Errorf("isPublicBind(%q) = true, want false", h)
		}
	}
	for _, h := range []string{"0.0.0.0", "192.168.1.10", "::"} {
		if !isPublicBind(h) {
			t.Errorf("isPublicBind(%q) = false, want true", h)
		}
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
