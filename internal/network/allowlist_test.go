package network

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// TestParseAllowlist covers the aliases shared by `claw network` and
// `claw install --allowed-cidrs`. They exist so the headless case does not
// require remembering three RFC1918 prefixes, which is where operators
// otherwise reach for 0.0.0.0/0 — an IPv4 prefix that refuses IPv6 clients.
func TestParseAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"private alias", "private", config.PrivateNetworkCIDRs},
		{"lan alias", "lan", config.PrivateNetworkCIDRs},
		{"any alias", "any", []string{config.AllowAnyAddress}},
		{"all alias", "all", []string{config.AllowAnyAddress}},
		{"none alias", "none", []string{}},
		{"loopback alias", "loopback", []string{}},
		{"alias is case-insensitive", "PRIVATE", config.PrivateNetworkCIDRs},
		{"alias tolerates spacing", "  any  ", []string{config.AllowAnyAddress}},
		{"single cidr", "192.168.1.0/24", []string{"192.168.1.0/24"}},
		{"comma separated", "10.0.0.0/8, 192.168.1.0/24", []string{"10.0.0.0/8", "192.168.1.0/24"}},
		{"wildcard passes through", "*", []string{"*"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseAllowlist(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("ParseAllowlist(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ParseAllowlist(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}

	// An alias must not alias the shared slice, or a later edit would corrupt it.
	got := ParseAllowlist("private")
	got[0] = "0.0.0.0/0"
	if config.PrivateNetworkCIDRs[0] == "0.0.0.0/0" {
		t.Fatal("ParseAllowlist aliased config.PrivateNetworkCIDRs")
	}
}

// TestParseAllowlistBareDefault pins the recovery path: `claw network` with no
// argument must open the private LAN ranges, since that is the whole point of
// the command — an operator locked out of the WebUI has no other way back in
// without editing config.json.
func TestParseAllowlistBareDefault(t *testing.T) {
	got := ParseAllowlist("private")
	if len(got) != len(config.PrivateNetworkCIDRs) {
		t.Fatalf("bare default = %v, want %v", got, config.PrivateNetworkCIDRs)
	}
	if err := config.ValidateAllowedCIDRs(got); err != nil {
		t.Fatalf("bare default does not validate: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cidrs []string
		want  string
	}{
		{"empty", nil, "none — loopback only"},
		{"wildcard", []string{"*"}, "[*] — any address, IPv4 and IPv6"},
		{"explicit", []string{"192.168.1.0/24"}, "[192.168.1.0/24]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Describe(tc.cidrs); got != tc.want {
				t.Fatalf("Describe(%v) = %q, want %q", tc.cidrs, got, tc.want)
			}
		})
	}
}
