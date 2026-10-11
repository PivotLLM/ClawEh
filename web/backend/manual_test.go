package webserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

// manualServer mounts the embedded manual behind the WebUI login, as the
// gateway does, and returns it with a valid session cookie.
func manualServer(t *testing.T) (http.Handler, *http.Cookie) {
	t.Helper()
	credPath := admin.Path(t.TempDir())
	if err := admin.Write(credPath, "alice", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	store := middleware.NewAuthStore(credPath)
	id, ok := store.Login("alice", "correct horse battery staple")
	if !ok {
		t.Fatal("login failed")
	}
	mux := http.NewServeMux()
	RegisterManualRoutes(mux)
	RegisterEmbedRoutes(mux)
	h := middleware.Auth(
		func() *middleware.AuthStore { return store },
		func() *middleware.AuthExempt { return nil },
		mux,
	)
	return h, &http.Cookie{Name: middleware.SessionCookieInsecure, Value: id}
}

func serveManual(h http.Handler, method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "127.0.0.1:4321"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestManual_ServesIndex needs the manual built into web/backend/manual
// (make manual; make test and make build do it first).
func TestManual_ServesIndex(t *testing.T) {
	h, cookie := manualServer(t)

	rec := serveManual(h, http.MethodGet, "/manual/", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /manual/ = %d, want 200 (is the manual built? run: make manual)", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "ClawEh Manual") {
		t.Fatalf("GET /manual/ did not return the manual index:\n%.300s", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("index Cache-Control = %q, want no-cache", got)
	}

	rec = serveManual(h, http.MethodGet, "/manual/getting-started/overview.html", cookie)
	if rec.Code != http.StatusOK {
		t.Errorf("GET a manual page = %d, want 200", rec.Code)
	}
}

func TestManual_RedirectsBarePath(t *testing.T) {
	h, cookie := manualServer(t)
	rec := serveManual(h, http.MethodGet, "/manual", cookie)
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/manual/" {
		t.Fatalf("GET /manual = %d Location %q, want 301 to /manual/", rec.Code, rec.Header().Get("Location"))
	}
}

func TestManual_NotFound(t *testing.T) {
	h, cookie := manualServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/manual/no-such-page.html"},
		{http.MethodGet, "/manual/no-such-dir/"},
		{http.MethodGet, "/manual/assets/"}, // a directory without index.html: no listing
		{http.MethodGet, "/manual/.gitkeep"},
		{http.MethodPost, "/manual/"},
	} {
		rec := serveManual(h, tc.method, tc.path, cookie)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}

func TestManual_RequiresLogin(t *testing.T) {
	h, _ := manualServer(t)
	for _, path := range []string{"/manual", "/manual/", "/manual/getting-started/overview.html"} {
		if rec := serveManual(h, http.MethodGet, path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session = %d, want 401", path, rec.Code)
		}
	}
}

func TestManual_CacheControl(t *testing.T) {
	h, cookie := manualServer(t)
	rec := serveManual(h, http.MethodGet, "/manual/", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /manual/ = %d", rec.Code)
	}
	// Hashed VitePress assets may be cached for good; pick one the index names.
	body := rec.Body.String()
	i := strings.Index(body, "/manual/assets/")
	if i < 0 {
		t.Fatal("index names no asset under /manual/assets/")
	}
	asset := body[i:]
	asset = asset[:strings.IndexAny(asset, "\"' ")]
	rec = serveManual(h, http.MethodGet, asset, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", asset, rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("asset Cache-Control = %q, want immutable", got)
	}
}
