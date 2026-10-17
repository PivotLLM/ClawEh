package api_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	webserver "github.com/PivotLLM/ClawEh/web/backend"
)

// The Tools page and its API are gone. Their old endpoints must answer 404
// through the full WebUI mux, never the SPA's index page, which would read as
// a success to a client that only checks the status.
func TestRemovedToolsEndpointsAre404(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CLAW_HOME", filepath.Join(tmp, ".claw"))
	configPath := filepath.Join(tmp, "config.json")
	if err := config.SaveConfig(configPath, config.DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	mux := http.NewServeMux()
	webserver.New(webserver.Options{ConfigPath: configPath}).RegisterRoutes(mux)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/tools"},
		{http.MethodPut, "/api/tools/x/state"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"enabled":true}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
				t.Fatalf("Content-Type = %q, want no HTML page", ct)
			}
			if body := strings.ToLower(rec.Body.String()); strings.Contains(body, "<html") || strings.Contains(body, "<!doctype") {
				t.Fatalf("body is an HTML page: %s", rec.Body.String())
			}
		})
	}
}
