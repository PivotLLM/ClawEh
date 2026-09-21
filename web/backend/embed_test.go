package webserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnknownAPIPathStays404(t *testing.T) {
	mux := http.NewServeMux()
	RegisterEmbedRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/not-found", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestMissingAssetStays404(t *testing.T) {
	mux := http.NewServeMux()
	RegisterEmbedRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/assets/not-found.js", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

// TestCacheControl: the SPA entry and other unhashed files must always be
// revalidated so a deploy is picked up; the content-hashed assets/ files may
// be cached for good.
func TestCacheControl(t *testing.T) {
	cases := []struct{ path, want string }{
		{"", "no-cache"},
		{"agents", "no-cache"},
		{"favicon.svg", "no-cache"},
		{"assets/index-abc123.js", "public, max-age=31536000, immutable"},
	}
	for _, tc := range cases {
		if got := cacheControl(tc.path); got != tc.want {
			t.Errorf("cacheControl(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
