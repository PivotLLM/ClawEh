package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
