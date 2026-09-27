package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// The IP allowlist behind TrustedProxy judges the forwarded client when the
// peer is a trusted proxy, and the peer itself otherwise.
func TestTrustedProxy_AllowlistUsesForwardedAddress(t *testing.T) {
	trusted, err := config.CompileTrustedProxies([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	allow, err := CompileAllowlist([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	chain := TrustedProxy(func() *config.TrustedProxySet { return trusted },
		IPAllowlist(func() *Allowlist { return allow }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.RemoteAddr
		})))
	for _, tc := range []struct {
		name, peer, realIP, xff string
		wantStatus              int
		wantSeen                string
	}{
		{"trusted proxy, allowed client", "127.0.0.1:4000", "10.1.2.3", "", http.StatusOK, "10.1.2.3"},
		{"trusted proxy, X-Forwarded-For client", "127.0.0.1:4000", "", "10.4.5.6, 127.0.0.1", http.StatusOK, "10.4.5.6"},
		// Loopback is always allowed, but the client here is not loopback.
		{"trusted proxy, refused client", "127.0.0.1:4000", "203.0.113.7", "", http.StatusForbidden, ""},
		{"untrusted peer cannot claim an allowed address", "203.0.113.9:4000", "10.1.2.3", "", http.StatusForbidden, ""},
		{"trusted proxy without headers is itself", "127.0.0.1:4000", "", "", http.StatusOK, "127.0.0.1:4000"},
	} {
		seen = ""
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tc.peer
		if tc.realIP != "" {
			req.Header.Set("X-Real-IP", tc.realIP)
		}
		if tc.xff != "" {
			req.Header.Set("X-Forwarded-For", tc.xff)
		}
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		if rec.Code != tc.wantStatus || seen != tc.wantSeen {
			t.Errorf("%s: status %d seen %q, want %d %q", tc.name, rec.Code, seen, tc.wantStatus, tc.wantSeen)
		}
	}
}
