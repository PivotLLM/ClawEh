package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEffectiveAllowedCIDRs(t *testing.T) {
	// Empty means loopback only. It must NOT fall back to the RFC1918 set: the
	// WebUI/API behind this allowlist has no operator auth, so a config that
	// omits the key must not hand the LAN read/write access to it.
	t.Run("empty means loopback only", func(t *testing.T) {
		g := GatewayConfig{}
		if got := g.EffectiveAllowedCIDRs(); len(got) != 0 {
			t.Fatalf("EffectiveAllowedCIDRs() = %v, want empty (loopback only)", got)
		}
	})

	t.Run("private ranges are opt-in, not a default", func(t *testing.T) {
		g := GatewayConfig{AllowedCIDRs: PrivateNetworkCIDRs}
		got := g.EffectiveAllowedCIDRs()
		if len(got) != len(PrivateNetworkCIDRs) {
			t.Fatalf("EffectiveAllowedCIDRs() = %v, want %v", got, PrivateNetworkCIDRs)
		}
		// Must be a copy: mutating the result must not corrupt the shared slice.
		got[0] = "0.0.0.0/0"
		if PrivateNetworkCIDRs[0] == "0.0.0.0/0" {
			t.Fatal("EffectiveAllowedCIDRs() aliased PrivateNetworkCIDRs")
		}
	})

	t.Run("set returned verbatim", func(t *testing.T) {
		want := []string{"192.168.1.0/24", "10.1.0.0/16"}
		g := GatewayConfig{AllowedCIDRs: want}
		got := g.EffectiveAllowedCIDRs()
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("EffectiveAllowedCIDRs() = %v, want %v", got, want)
		}
		// Copy semantics: mutating the result must not touch the config slice.
		got[0] = "0.0.0.0/0"
		if g.AllowedCIDRs[0] == "0.0.0.0/0" {
			t.Fatal("EffectiveAllowedCIDRs() aliased the config slice")
		}
	})
}

func TestPrivateNetworkCIDRsDefault(t *testing.T) {
	want := map[string]bool{
		"10.0.0.0/8":     true,
		"172.16.0.0/12":  true,
		"192.168.0.0/16": true,
	}
	if len(PrivateNetworkCIDRs) != len(want) {
		t.Fatalf("PrivateNetworkCIDRs = %v, want the RFC1918 ranges", PrivateNetworkCIDRs)
	}
	for _, c := range PrivateNetworkCIDRs {
		if !want[c] {
			t.Errorf("unexpected default CIDR %q", c)
		}
	}
}

// TestLoadConfig_IgnoresLauncherConfig: the retired launcher-config.json is
// no longer read, so an allowlist in it must not reach gateway.allowed_cidrs.
func TestLoadConfig_IgnoresLauncherConfig(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "launcher-config.json")
	if err := os.WriteFile(stale, []byte(`{"allowed_cidrs":["192.168.5.0/24"]}`), 0o600); err != nil {
		t.Fatalf("write launcher-config.json: %v", err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	for _, c := range cfg.Gateway.AllowedCIDRs {
		if c == "192.168.5.0/24" {
			t.Fatalf("launcher-config.json allowlist was adopted: %v", cfg.Gateway.AllowedCIDRs)
		}
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("launcher-config.json must be left for the operator to delete: %v", err)
	}
}

// TestValidateAllowedCIDRs_AcceptsWildcard covers the "allow any address" entry.
// It is not a CIDR, so validation has to let it through explicitly or an
// operator cannot save the one value that means "open this up".
func TestValidateAllowedCIDRs_AcceptsWildcard(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cidrs   []string
		wantErr bool
	}{
		{"wildcard alone", []string{AllowAnyAddress}, false},
		{"wildcard with a CIDR", []string{"192.168.1.0/24", AllowAnyAddress}, false},
		{"wildcard with surrounding space", []string{" * "}, false},
		{"plain CIDRs", []string{"10.0.0.0/8", "::/0"}, false},
		{"empty list", nil, false},
		{"a real typo is still rejected", []string{"192.168.1.0/24", "not-a-cidr"}, true},
		{"a partial wildcard is not the wildcard", []string{"*.*.*.*"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAllowedCIDRs(tc.cidrs)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateAllowedCIDRs(%v) = nil, want an error", tc.cidrs)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateAllowedCIDRs(%v) = %v, want nil", tc.cidrs, err)
			}
		})
	}
}
