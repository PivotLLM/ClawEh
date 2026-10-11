package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/clock"
)

// TestProxyWatch records a forwarded request from an untrusted loopback peer
// and nothing else, and never changes how the request is handled.
func TestProxyWatch(t *testing.T) {
	start := time.Date(2026, 10, 10, 14, 3, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, peer, header, value string
		trusted                   []string
		wantPeer                  string
	}{
		{"untrusted loopback with X-Forwarded-For", "127.0.0.1:4000", "X-Forwarded-For", "203.0.113.7", nil, "127.0.0.1"},
		{"untrusted loopback with X-Real-IP", "127.0.0.1:4000", "X-Real-IP", "203.0.113.7", nil, "127.0.0.1"},
		{"untrusted IPv6 loopback with Forwarded", "[::1]:4000", "Forwarded", "for=203.0.113.7", nil, "::1"},
		{"untrusted loopback, other proxy trusted", "127.0.0.1:4000", "X-Forwarded-For", "203.0.113.7", []string{"10.0.0.5"}, "127.0.0.1"},
		{"trusted loopback proxy", "127.0.0.1:4000", "X-Forwarded-For", "203.0.113.7", []string{"127.0.0.1"}, ""},
		{"loopback without forwarding headers", "127.0.0.1:4000", "", "", nil, ""},
		{"network peer with X-Forwarded-For", "203.0.113.9:4000", "X-Forwarded-For", "10.1.2.3", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trusted, err := config.CompileTrustedProxies(tc.trusted)
			if err != nil {
				t.Fatal(err)
			}
			fake := clock.NewFake(start)
			watch := &ProxyWatch{Clock: fake}
			var seen string
			chain := TrustedProxy(func() *config.TrustedProxySet { return trusted }, watch,
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.RemoteAddr }))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.peer
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			chain.ServeHTTP(httptest.NewRecorder(), req)

			peer, at := watch.LastSeen()
			if peer != tc.wantPeer {
				t.Errorf("peer = %q, want %q", peer, tc.wantPeer)
			}
			if tc.wantPeer != "" && !at.Equal(start) {
				t.Errorf("seen at %v, want %v", at, start)
			}
			if tc.wantPeer != "" && seen != tc.peer {
				t.Errorf("request handled as %q, want the peer %q unchanged", seen, tc.peer)
			}
		})
	}
}

// TestProxyWatch_LastSeenAdvances: a later sighting replaces the time.
func TestProxyWatch_LastSeenAdvances(t *testing.T) {
	start := time.Date(2026, 10, 10, 14, 3, 0, 0, time.UTC)
	fake := clock.NewFake(start)
	watch := &ProxyWatch{Clock: fake}
	chain := TrustedProxy(func() *config.TrustedProxySet { return nil }, watch, http.NotFoundHandler())
	send := func() {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:4000"
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
		chain.ServeHTTP(httptest.NewRecorder(), req)
	}
	send()
	fake.Advance(time.Hour)
	send()
	if _, at := watch.LastSeen(); !at.Equal(start.Add(time.Hour)) {
		t.Errorf("seen at %v, want %v", at, start.Add(time.Hour))
	}
	var none *ProxyWatch
	if peer, at := none.LastSeen(); peer != "" || !at.IsZero() {
		t.Errorf("nil watch = %q %v", peer, at)
	}
}
