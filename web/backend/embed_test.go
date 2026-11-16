package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/internal/testbinary"
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

// templatedFS is a frontend build whose index.html and manifest carry the
// name placeholders, as web/frontend's do.
func templatedFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html": {Data: []byte(`<title>{{PRODUCT_NAME}}</title>` +
			`<meta name="product-name" content="{{PRODUCT_NAME}}">` +
			`<meta name="product-short-name" content="{{PRODUCT_SHORT_NAME}}">` +
			`<meta name="product-binary-name" content="{{PRODUCT_BINARY_NAME}}">`)},
		"site.webmanifest": {Data: []byte(`{"name": "{{PRODUCT_NAME}}", "short_name": "{{PRODUCT_SHORT_NAME}}"}`)},
		"favicon.svg":      {Data: []byte(`<svg/>`)},
	}
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

// TestIndex_CarriesProductNames: the page has the names from its first byte —
// the title and the meta tags the frontend reads — on every SPA path, and is
// revalidated on each load.
func TestIndex_CarriesProductNames(t *testing.T) {
	testbinary.Set(t, "alice-claw")
	h := spaHandler(templatedFS())
	want := `<title>` + app.Name() + `</title>` +
		`<meta name="product-name" content="` + app.Name() + `">` +
		`<meta name="product-short-name" content="` + app.ShortName() + `">` +
		`<meta name="product-binary-name" content="alice-claw">`
	for _, p := range []string{"/", "/agents", "/index.html", "/login?next=%2Fagents"} {
		rr := get(t, h, p)
		if rr.Code != http.StatusOK || rr.Body.String() != want {
			t.Errorf("GET %s = %d %q, want %q", p, rr.Code, rr.Body.String(), want)
		}
		if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", p, cc)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s Content-Type = %q", p, ct)
		}
	}
}

// TestIndex_EscapesNames: a name is text in the page, never markup.
func TestIndex_EscapesNames(t *testing.T) {
	testbinary.Set(t, `a"b<c`)
	rr := get(t, spaHandler(templatedFS()), "/")
	if !strings.Contains(rr.Body.String(), `content="a&#34;b&lt;c"`) {
		t.Errorf("binary name not escaped: %s", rr.Body.String())
	}
}

// TestManifest_CarriesProductNames: the web app manifest names the product
// from the server too, as JSON.
func TestManifest_CarriesProductNames(t *testing.T) {
	rr := get(t, spaHandler(templatedFS()), "/site.webmanifest")
	var m struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest %q: %v", rr.Body.String(), err)
	}
	if m.Name != app.Name() || m.ShortName != app.ShortName() {
		t.Errorf("manifest names = %+v", m)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// TestJSONStringContent covers the manifest escaping.
func TestJSONStringContent(t *testing.T) {
	if got := jsonStringContent("a\"b\\c\n"); got != `a\"b\\c\u000a` {
		t.Errorf("jsonStringContent = %q", got)
	}
}

// TestSPA_PlainFilesUnchanged: files without placeholders are served as they
// are.
func TestSPA_PlainFilesUnchanged(t *testing.T) {
	rr := get(t, spaHandler(templatedFS()), "/favicon.svg")
	if rr.Code != http.StatusOK || rr.Body.String() != `<svg/>` {
		t.Errorf("GET /favicon.svg = %d %q", rr.Code, rr.Body.String())
	}
}
