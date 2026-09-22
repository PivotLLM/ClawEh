// ClawEh
// License: MIT

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

// TestReportPDF: the endpoint streams a PDF inline, uncached, built from the
// loaded config. The body is checked for the PDF signature rather than its
// exact bytes.
func TestReportPDF(t *testing.T) {
	configPath := setupTestEnv(t)
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/report/pdf", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `inline; filename="claweh-report-`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if !strings.HasPrefix(rr.Body.String(), "%PDF-") {
		t.Errorf("body does not start with a PDF signature: %q", rr.Body.String()[:min(8, rr.Body.Len())])
	}
	// The fixture's provider key must never reach the report.
	if strings.Contains(rr.Body.String(), "sk-default") {
		t.Error("provider API key leaked into the PDF")
	}
}

// TestReportEnvironment: process facts are filled, never left empty.
func TestReportEnvironment(t *testing.T) {
	env := reportEnvironment("/tmp/config.json", "/tmp/data", testNow)
	for name, v := range map[string]string{
		"User": env.User, "Group": env.Group, "Hostname": env.Hostname,
		"Executable": env.Executable, "Version": env.Version, "OS": env.OS, "Arch": env.Arch,
	} {
		if v == "" {
			t.Errorf("%s is empty", name)
		}
	}
	if env.ConfigPath != "/tmp/config.json" || env.DataDir != "/tmp/data" || !env.Now.Equal(testNow) {
		t.Errorf("environment did not carry the caller's values: %+v", env)
	}
}
