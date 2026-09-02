package network

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

// seedConfig points CLAW_HOME at a temp dir holding a default config, and
// returns its path. Every test here writes a real config file, because the
// bug this command exists to recover from is a config-file problem and a test
// that stubs the file out would not see it.
func seedConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	path := filepath.Join(home, "config.json")
	if err := config.SaveConfig(path, config.DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	if got := internal.GetConfigPath(); got != path {
		t.Fatalf("GetConfigPath() = %q, want %q", got, path)
	}
	return path
}

func TestApplyAllowlistRoundTrip(t *testing.T) {
	seedConfig(t)

	for _, tc := range []struct {
		name string
		spec string
		want []string
	}{
		{"private ranges", "private", config.PrivateNetworkCIDRs},
		{"single subnet", "192.168.1.0/24", []string{"192.168.1.0/24"}},
		{"two subnets", "10.0.0.0/8,192.168.1.0/24", []string{"10.0.0.0/8", "192.168.1.0/24"}},
		{"wildcard", "any", []string{config.AllowAnyAddress}},
		{"back to loopback", "none", nil},
		{"idempotent re-apply", "none", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ApplyAllowlist(ParseAllowlist(tc.spec)); err != nil {
				t.Fatalf("ApplyAllowlist(%q) error = %v", tc.spec, err)
			}
			got, err := CurrentAllowlist()
			if err != nil {
				t.Fatalf("CurrentAllowlist() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("after %q: allowlist = %v, want %v", tc.spec, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("after %q: allowlist = %v, want %v", tc.spec, got, tc.want)
				}
			}
		})
	}
}

// TestApplyAllowlistRejectsInvalidWithoutWriting is the property that keeps a
// typo from making things worse: a rejected list must leave the config exactly
// as it was, not half-written and not cleared. An operator running this command
// is already locked out; a failed run must not also destroy their config.
func TestApplyAllowlistRejectsInvalidWithoutWriting(t *testing.T) {
	path := seedConfig(t)
	if _, err := ApplyAllowlist(ParseAllowlist("192.168.1.0/24")); err != nil {
		t.Fatalf("ApplyAllowlist() error = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"192.168.1.0", "not-a-cidr", "192.168.1.0/33", "10.0.0.0/8,garbage"} {
		if _, err := ApplyAllowlist(ParseAllowlist(bad)); err == nil {
			t.Fatalf("ApplyAllowlist(%q) accepted an invalid list", bad)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatalf("ApplyAllowlist(%q) rewrote the config after rejecting it", bad)
		}
	}

	got, err := CurrentAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "192.168.1.0/24" {
		t.Fatalf("allowlist = %v, want the pre-existing [192.168.1.0/24]", got)
	}
}

// TestApplyAllowlistPreservesTheRestOfTheConfig pins that this is a targeted
// edit and not a rewrite from defaults. It runs against a live gateway, so
// dropping a bot token or resetting the port would be a far worse outcome than
// the lockout it is fixing.
func TestApplyAllowlistPreservesTheRestOfTheConfig(t *testing.T) {
	path := seedConfig(t)

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Gateway.Host = "0.0.0.0"
	cfg.Gateway.Port = 18999
	cfg.Channels.WebUI.Enabled = true
	cfg.Channels.WebUI.Token = "sentinel-token-value"
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyAllowlist(ParseAllowlist("private")); err != nil {
		t.Fatalf("ApplyAllowlist() error = %v", err)
	}

	after, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Gateway.Host != "0.0.0.0" {
		t.Errorf("Gateway.Host = %q, want it untouched", after.Gateway.Host)
	}
	if after.Gateway.Port != 18999 {
		t.Errorf("Gateway.Port = %d, want it untouched", after.Gateway.Port)
	}
	if after.Channels.WebUI.Token != "sentinel-token-value" {
		t.Errorf("WebUI.Token = %q, want it untouched — the allowlist edit must not touch credentials", after.Channels.WebUI.Token)
	}
	if len(after.Gateway.AllowedCIDRs) != len(config.PrivateNetworkCIDRs) {
		t.Errorf("AllowedCIDRs = %v, want %v", after.Gateway.AllowedCIDRs, config.PrivateNetworkCIDRs)
	}
}

// TestAppliedAllowlistIsAcceptedByTheGateway closes the loop between the two
// halves: whatever this command writes must compile in the middleware that
// enforces it. A value the CLI accepts but the gateway then rejects would fail
// the reload and leave the operator locked out with a config that looks right.
func TestAppliedAllowlistIsAcceptedByTheGateway(t *testing.T) {
	seedConfig(t)
	for _, spec := range []string{"private", "lan", "any", "all", "none", "loopback", "192.168.1.0/24", "10.0.0.0/8,172.16.0.0/12", "::/0", "*"} {
		cidrs := ParseAllowlist(spec)
		if _, err := ApplyAllowlist(cidrs); err != nil {
			t.Fatalf("ApplyAllowlist(%q) error = %v", spec, err)
		}
		stored, err := CurrentAllowlist()
		if err != nil {
			t.Fatal(err)
		}
		if err := config.ValidateAllowedCIDRs(stored); err != nil {
			t.Fatalf("%q stored %v, which the config layer rejects: %v", spec, stored, err)
		}
		// The middleware is the thing that actually enforces it on the live
		// listener, so compile through that too rather than trusting the two
		// validators to agree.
		if _, err := middleware.CompileAllowlist(stored); err != nil {
			t.Fatalf("%q stored %v, which the enforcing middleware rejects: %v", spec, stored, err)
		}
	}
}
