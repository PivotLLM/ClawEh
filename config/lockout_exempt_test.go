package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileLockoutExempt_Contains(t *testing.T) {
	set, err := CompileLockoutExempt([]string{"203.0.113.5", " 10.1.0.0/16 ", "2001:db8::/32", "192.0.2.77/24"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"203.0.113.5", true},     // bare IPv4 is a /32
		{"203.0.113.6", false},    // ...and nothing more
		{"10.1.255.9", true},      // inside a CIDR (surrounding space trimmed)
		{"10.2.0.1", false},       // outside it
		{"192.0.2.200", true},     // host bits in a CIDR are masked off
		{"2001:db8::42", true},    // IPv6 CIDR
		{"::ffff:10.1.0.1", true}, // IPv4-mapped IPv6 matches the IPv4 entry
		{"127.0.0.1", true},       // loopback always
		{"::1", true},
		{"not-an-ip", false},
		{"", false},
	} {
		if got := set.Contains(tc.ip); got != tc.want {
			t.Errorf("Contains(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestLockoutExemptSet_EmptyAndNilAreLoopbackOnly(t *testing.T) {
	empty, err := CompileLockoutExempt(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range []*LockoutExemptSet{empty, nil} {
		if !set.Contains("127.0.0.1") || !set.Contains("::1") || !set.Contains("127.8.9.10") {
			t.Fatal("loopback not exempt")
		}
		if set.Contains("192.0.2.1") {
			t.Fatal("non-loopback exempt with an empty list")
		}
	}
}

func TestCompileLockoutExempt_RejectsBadEntries(t *testing.T) {
	for _, bad := range []string{"", "*", "10.0.0.0/33", "300.1.1.1", "host.example", "fe80::1%eth0", "10.0.0.1-10.0.0.9"} {
		_, err := CompileLockoutExempt([]string{"10.0.0.1", bad})
		if err == nil {
			t.Errorf("entry %q accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "gateway.lockout_exempt") {
			t.Errorf("entry %q: error %q does not name the key", bad, err)
		}
	}
}

func TestGatewayValidate_LockoutExempt(t *testing.T) {
	if err := (GatewayConfig{LockoutExempt: []string{"192.0.2.1", "10.0.0.0/8"}}).Validate(); err != nil {
		t.Fatalf("valid list rejected: %v", err)
	}
	if err := (GatewayConfig{LockoutExempt: []string{"nope"}}).Validate(); err == nil {
		t.Fatal("invalid entry accepted")
	}
}

func TestLoadConfig_RejectsBadLockoutExempt(t *testing.T) {
	t.Setenv("CLAW_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"gateway":{"lockout_exempt":["10.0.0.0/99"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "gateway.lockout_exempt") {
		t.Fatalf("LoadConfig error = %v, want a gateway.lockout_exempt error", err)
	}
}
