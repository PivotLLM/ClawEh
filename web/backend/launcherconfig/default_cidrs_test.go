package launcherconfig

import "testing"

// The default config must ship the RFC1918 allowlist so a fresh install is
// locked to loopback + the private network out of the box.
func TestDefaultIncludesPrivateCIDRs(t *testing.T) {
	want := map[string]bool{
		"10.0.0.0/8":     true,
		"172.16.0.0/12":  true,
		"192.168.0.0/16": true,
	}
	got := Default().AllowedCIDRs
	if len(got) != len(want) {
		t.Fatalf("Default().AllowedCIDRs = %v, want the RFC1918 ranges", got)
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("unexpected default CIDR %q", c)
		}
	}
}
