package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSecurityHeaders pins the hardening headers on every response and the
// no-store rule for /api/* only — the SPA's assets keep their own caching.
func TestSecurityHeaders(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range []struct {
		path      string
		wantCache string
	}{
		{"/api/config", "no-store"},
		{"/api/sessions/x", "no-store"},
		{"/", ""},
		{"/assets/app.js", ""},
		{"/webui/ws", ""},
		{"/health", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			for k, want := range map[string]string{
				"Content-Security-Policy": "frame-ancestors 'none'",
				"X-Content-Type-Options":  "nosniff",
				"Referrer-Policy":         "no-referrer",
				"Cache-Control":           tc.wantCache,
			} {
				if got := rec.Header().Get(k); got != want {
					t.Errorf("%s: %s = %q, want %q", tc.path, k, got, want)
				}
			}
		})
	}
}

// TestSecurityHeaders_HandlerMayOverride: the headers are set before the
// handler runs, so a handler with its own Cache-Control (the SPA asset server)
// still wins.
func TestSecurityHeaders_HandlerMayOverride(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q, want the handler's value", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}
