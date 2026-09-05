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

func TestMigrateLauncherConfig(t *testing.T) {
	writeLauncher := func(t *testing.T, dir, body string) string {
		t.Helper()
		p := filepath.Join(dir, "launcher-config.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write launcher-config.json: %v", err)
		}
		return p
	}

	t.Run("adopts allowlist each load without persisting or deleting", func(t *testing.T) {
		dir := t.TempDir()
		lcPath := writeLauncher(t, dir, `{"port":18790,"public":true,"allowed_cidrs":["192.168.5.0/24","10.1.0.0/16"]}`)
		cfg := &Config{}

		migrateLauncherConfig(filepath.Join(dir, "config.json"), cfg)

		got := cfg.Gateway.AllowedCIDRs
		if len(got) != 2 || got[0] != "192.168.5.0/24" || got[1] != "10.1.0.0/16" {
			t.Fatalf("Gateway.AllowedCIDRs = %v, want [192.168.5.0/24 10.1.0.0/16]", got)
		}
		// The legacy file is intentionally LEFT in place (persisting at load would
		// bake env-overlaid values into config.json), so the allowlist survives a
		// restart by re-adoption. A second load re-adopts identically.
		if _, err := os.Stat(lcPath); err != nil {
			t.Fatalf("launcher-config.json should be left in place for re-adoption, stat err=%v", err)
		}
		cfg2 := &Config{}
		migrateLauncherConfig(filepath.Join(dir, "config.json"), cfg2)
		if len(cfg2.Gateway.AllowedCIDRs) != 2 {
			t.Fatalf("re-adoption failed: %v", cfg2.Gateway.AllowedCIDRs)
		}
	})

	t.Run("no legacy file is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &Config{}
		migrateLauncherConfig(filepath.Join(dir, "config.json"), cfg)
		if cfg.Gateway.AllowedCIDRs != nil {
			t.Fatalf("Gateway.AllowedCIDRs = %v, want nil", cfg.Gateway.AllowedCIDRs)
		}
	})

	t.Run("existing gateway allowlist is not overwritten", func(t *testing.T) {
		dir := t.TempDir()
		lcPath := writeLauncher(t, dir, `{"allowed_cidrs":["172.16.0.0/12"]}`)
		cfg := &Config{Gateway: GatewayConfig{AllowedCIDRs: []string{"192.168.9.0/24"}}}

		migrateLauncherConfig(filepath.Join(dir, "config.json"), cfg)

		if len(cfg.Gateway.AllowedCIDRs) != 1 || cfg.Gateway.AllowedCIDRs[0] != "192.168.9.0/24" {
			t.Fatalf("Gateway.AllowedCIDRs = %v, want [192.168.9.0/24]", cfg.Gateway.AllowedCIDRs)
		}
		// Gated on empty gateway allowlist, so the legacy file is left untouched.
		if _, err := os.Stat(lcPath); err != nil {
			t.Fatalf("launcher-config.json should be left in place, stat err=%v", err)
		}
	})
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
