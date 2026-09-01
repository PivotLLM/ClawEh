package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// TestIPAllowlist_EmptyMeansLoopbackOnly is the security property behind the
// out-of-the-box posture: an unset allowlist must not mean allow-all. The
// handler behind this middleware is the unauthenticated WebUI/API, so the
// opposite reading would expose config and credentials to the whole network to
// anyone whose config simply omits the key.
func TestIPAllowlist_EmptyMeansLoopbackOnly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remoteAddr string
		wantServed bool
	}{
		{"loopback v4", "127.0.0.1:5000", true},
		{"loopback v6", "[::1]:5000", true},
		{"LAN peer", "192.168.1.50:5000", false},
		{"other private range", "10.1.2.3:5000", false},
		{"public address", "203.0.113.9:5000", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			served := false
			h, err := IPAllowlist(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served = true }))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			req.RemoteAddr = tc.remoteAddr
			h.ServeHTTP(httptest.NewRecorder(), req)

			if served != tc.wantServed {
				t.Fatalf("%s served = %v, want %v (empty allowlist must be loopback-only)", tc.remoteAddr, served, tc.wantServed)
			}
		})
	}
}

// TestIPAllowlist_ExplicitCIDRsWiden covers the operator's escape hatch: a
// configured allowlist grants exactly what it names, and 0.0.0.0/0 grants all.
func TestIPAllowlist_ExplicitCIDRsWiden(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cidrs      []string
		remoteAddr string
		wantServed bool
	}{
		{"LAN subnet allows its own", []string{"192.168.1.0/24"}, "192.168.1.50:5000", true},
		{"LAN subnet excludes others", []string{"192.168.1.0/24"}, "192.168.2.50:5000", false},
		{"LAN subnet excludes public", []string{"192.168.1.0/24"}, "203.0.113.9:5000", false},
		{"RFC1918 set allows private", []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}, "10.1.2.3:5000", true},
		{"RFC1918 set excludes public", []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}, "203.0.113.9:5000", false},
		{"any address", []string{"0.0.0.0/0"}, "203.0.113.9:5000", true},
		{"loopback always allowed", []string{"192.168.1.0/24"}, "127.0.0.1:5000", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			served := false
			h, err := IPAllowlist(tc.cidrs, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served = true }))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			req.RemoteAddr = tc.remoteAddr
			h.ServeHTTP(httptest.NewRecorder(), req)

			if served != tc.wantServed {
				t.Fatalf("cidrs=%v addr=%s served = %v, want %v", tc.cidrs, tc.remoteAddr, served, tc.wantServed)
			}
		})
	}
}

// TestIPAllowlist_WildcardAllowsAnyAddress covers the "allow everything" escape
// hatch, and the reason it exists: 0.0.0.0/0 is an IPv4 prefix, so on a
// dual-stack host it refuses IPv6 clients. Someone reaching for "allow all"
// needs one entry that actually means it.
func TestIPAllowlist_WildcardAllowsAnyAddress(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cidrs      []string
		remoteAddr string
		wantServed bool
	}{
		{"wildcard allows public IPv4", []string{"*"}, "203.0.113.9:5000", true},
		{"wildcard allows public IPv6", []string{"*"}, "[2001:db8::1]:5000", true},
		{"wildcard allows private", []string{"*"}, "192.168.1.5:5000", true},
		{"wildcard allows loopback", []string{"*"}, "127.0.0.1:5000", true},
		{"wildcard alongside a CIDR still allows all", []string{"192.168.1.0/24", "*"}, "203.0.113.9:5000", true},

		// The trap the wildcard exists to avoid: these are correct, not bugs.
		{"0.0.0.0/0 allows IPv4", []string{"0.0.0.0/0"}, "203.0.113.9:5000", true},
		{"0.0.0.0/0 does NOT allow IPv6", []string{"0.0.0.0/0"}, "[2001:db8::1]:5000", false},
		{"::/0 allows IPv6", []string{"::/0"}, "[2001:db8::1]:5000", true},
		{"::/0 does NOT allow IPv4", []string{"::/0"}, "203.0.113.9:5000", false},
		{"both families together", []string{"0.0.0.0/0", "::/0"}, "[2001:db8::1]:5000", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			served := false
			h, err := IPAllowlist(tc.cidrs, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served = true }))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			req.RemoteAddr = tc.remoteAddr
			h.ServeHTTP(httptest.NewRecorder(), req)

			if served != tc.wantServed {
				t.Fatalf("cidrs=%v addr=%s served = %v, want %v", tc.cidrs, tc.remoteAddr, served, tc.wantServed)
			}
		})
	}
}

// TestAllowAnyAddressMatchesConfig pins the two constants together. They are
// declared separately so this package keeps its single dependency (logger)
// rather than pulling in config; the cost is that they could drift, and drift
// would mean the config layer accepting a value the middleware then rejects.
func TestAllowAnyAddressMatchesConfig(t *testing.T) {
	if AllowAnyAddress != config.AllowAnyAddress {
		t.Fatalf("middleware.AllowAnyAddress = %q but config.AllowAnyAddress = %q; they must match",
			AllowAnyAddress, config.AllowAnyAddress)
	}
}
