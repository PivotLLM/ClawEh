package network

import (
	"os"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal"
)

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func loadSeeded(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.LoadConfig(internal.GetConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The three flags map to the three bind settings and nothing else changes.
func TestApplyListeners(t *testing.T) {
	seedConfig(t)
	before := loadSeeded(t)

	if _, err := ApplyListeners(Listeners{HTTP: ScopeNetwork, HTTPS: config.TLSModeLocalhost, Device: ScopeNetwork}); err != nil {
		t.Fatalf("ApplyListeners: %v", err)
	}
	cfg := loadSeeded(t)
	if cfg.Gateway.Host != "0.0.0.0" {
		t.Errorf("gateway.host = %q, want 0.0.0.0", cfg.Gateway.Host)
	}
	if cfg.Gateway.TLS.Mode != config.TLSModeLocalhost {
		t.Errorf("gateway.tls.mode = %q, want localhost", cfg.Gateway.TLS.Mode)
	}
	if cfg.Channels.Device.Host != "0.0.0.0" {
		t.Errorf("channels.device.host = %q, want 0.0.0.0", cfg.Channels.Device.Host)
	}
	if cfg.Gateway.Port != before.Gateway.Port || len(cfg.Gateway.AllowedCIDRs) != len(before.Gateway.AllowedCIDRs) {
		t.Error("ApplyListeners changed settings it was not asked to")
	}

	// Back to localhost, one at a time; the others are untouched.
	if _, err := ApplyListeners(Listeners{HTTP: ScopeLocalhost}); err != nil {
		t.Fatal(err)
	}
	cfg = loadSeeded(t)
	if cfg.Gateway.Host != "127.0.0.1" || cfg.Channels.Device.Host != "0.0.0.0" || cfg.Gateway.TLS.Mode != config.TLSModeLocalhost {
		t.Errorf("after --http localhost: host=%q device=%q mode=%q", cfg.Gateway.Host, cfg.Channels.Device.Host, cfg.Gateway.TLS.Mode)
	}
}

// A bad word is refused before the file is touched.
func TestApplyListenersRejectsBadWords(t *testing.T) {
	path := seedConfig(t)
	before := readFile(t, path)
	for _, l := range []Listeners{{HTTP: "everyone"}, {HTTPS: "yes"}, {Device: "0.0.0.0"}} {
		if _, err := ApplyListeners(l); err == nil {
			t.Errorf("ApplyListeners(%+v) accepted a bad value", l)
		}
	}
	after := readFile(t, path)
	if string(before) != string(after) {
		t.Error("a rejected value still rewrote the config")
	}
}

// --show reports every listener and the allowlist, and writes nothing.
func TestCommandShowReportsListeners(t *testing.T) {
	path := seedConfig(t)
	// The default config leaves the device channel off; --show says so, and
	// this test wants the enabled shape.
	cfg := loadSeeded(t)
	cfg.Channels.Device.Enabled = true
	cfg.Channels.Device.Token = "shared" // a network device listener needs a secret
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyListeners(Listeners{HTTP: ScopeNetwork, Device: ScopeNetwork}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyAllowlist(ParseAllowlist("192.168.1.0/24")); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, path)

	out, err := runCommand(t, "--show")
	if err != nil {
		t.Fatalf("--show: %v", err)
	}
	for _, want := range []string{"HTTP:              network", "HTTPS:             network", "192.168.1.0/24", "Device listener:   network"} {
		if !strings.Contains(out, want) {
			t.Errorf("--show output lacks %q:\n%s", want, out)
		}
	}
	after := readFile(t, path)
	if string(before) != string(after) {
		t.Error("--show rewrote the config")
	}
}

// The recovery an operator types after a bad save: allowlist and HTTP scope
// in one command, with the restart requirement stated.
func TestCommandFlagsSetListeners(t *testing.T) {
	seedConfig(t)
	out, err := runCommand(t, "192.168.1.0/24", "--http", "network", "--device", "network")
	if err != nil {
		t.Fatalf("network with flags: %v", err)
	}
	cfg := loadSeeded(t)
	if cfg.Gateway.Host != "0.0.0.0" || cfg.Channels.Device.Host != "0.0.0.0" {
		t.Errorf("hosts = %q / %q, want 0.0.0.0 / 0.0.0.0", cfg.Gateway.Host, cfg.Channels.Device.Host)
	}
	if got := cfg.Gateway.AllowedCIDRs; len(got) != 1 || got[0] != "192.168.1.0/24" {
		t.Errorf("allowlist = %v", got)
	}
	if !strings.Contains(out, "restart") {
		t.Errorf("output does not say a restart is needed:\n%s", out)
	}

	// Flags alone leave the allowlist as it is.
	if _, err := runCommand(t, "--https", "off"); err != nil {
		t.Fatal(err)
	}
	cfg = loadSeeded(t)
	if got := cfg.Gateway.AllowedCIDRs; len(got) != 1 || got[0] != "192.168.1.0/24" {
		t.Errorf("a flags-only run changed the allowlist: %v", got)
	}
	if cfg.Gateway.TLS.Mode != config.TLSModeOff {
		t.Errorf("gateway.tls.mode = %q, want off", cfg.Gateway.TLS.Mode)
	}

	if _, err := runCommand(t, "--http", "sideways"); err == nil {
		t.Error("a bad --http value was accepted")
	}
}
