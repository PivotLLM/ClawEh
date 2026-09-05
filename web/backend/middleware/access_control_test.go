package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIPAllowlist_EmptyCIDRsRejectsNonLoopback pins the current contract: an
// empty allowlist is loopback-only. This replaces a test that asserted the
// opposite (empty = allow all) — the behaviour changed in 0.4.72 because the
// handler behind this middleware is the unauthenticated WebUI/API, and a config
// that merely omits the key must not expose it to the network.
func TestIPAllowlist_EmptyCIDRsRejectsNonLoopback(t *testing.T) {
	h := newAllowlistHandler(t, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("status = %d, want a rejection: an empty allowlist must be loopback-only", rec.Code)
	}
}

func TestIPAllowlist_RejectsOutsideCIDR(t *testing.T) {
	h := newAllowlistHandler(t, []string{"192.168.1.0/24"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.RemoteAddr = "10.0.0.8:1234"
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestIPAllowlist_AllowsInsideCIDR(t *testing.T) {
	h := newAllowlistHandler(t, []string{"192.168.1.0/24"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.88:1234"
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestIPAllowlist_AlwaysAllowsLoopback(t *testing.T) {
	h := newAllowlistHandler(t, []string{"192.168.1.0/24"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCompileAllowlist_InvalidCIDR(t *testing.T) {
	if _, err := CompileAllowlist([]string{"bad-cidr"}); err == nil {
		t.Fatal("CompileAllowlist() expected error for invalid CIDR")
	}
}

// newAllowlistHandler compiles cidrs and wraps next, the shape every test in
// this package needs. The allowlist is fixed for the handler's lifetime here;
// TestIPAllowlist_HotSwap covers the swapping path.
func newAllowlistHandler(t *testing.T, cidrs []string, next http.Handler) http.Handler {
	t.Helper()
	list, err := CompileAllowlist(cidrs)
	if err != nil {
		t.Fatalf("CompileAllowlist(%v) error = %v", cidrs, err)
	}
	return IPAllowlist(func() *Allowlist { return list }, next)
}
