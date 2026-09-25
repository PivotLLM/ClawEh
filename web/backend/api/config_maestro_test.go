// ClawEh
// License: MIT

package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

func patchMaestroConfig(t *testing.T, configPath, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	return rec
}

// TestHandlePatchConfig_MaestroBlockRoundTrips: the WebUI's patch of the
// maestro block is accepted and persisted as sent.
func TestHandlePatchConfig_MaestroBlockRoundTrips(t *testing.T) {
	configPath := setupTestEnv(t)

	rec := patchMaestroConfig(t, configPath, `{"agents":{"list":[{"id":"alice","maestro":{"enabled":true,"max_concurrent":2,"allow_parallel":false}}]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.AgentMaestro("alice")
	if m == nil || !m.Enabled || m.MaxConcurrent != 2 || m.ParallelAllowed() {
		t.Fatalf("persisted maestro = %+v", m)
	}

	// Turning it off keeps the runner settings.
	rec = patchMaestroConfig(t, configPath, `{"agents":{"list":[{"id":"alice","maestro":{"enabled":false,"max_concurrent":2,"allow_parallel":false}}]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if m := cfg.AgentMaestro("alice"); m == nil || m.Enabled || m.MaxConcurrent != 2 {
		t.Fatalf("after disable = %+v", m)
	}
}

// TestHandlePatchConfig_MaestroLegacyBooleanNotHonoured: a client still
// sending the boolean does not enable Maestro and does not break the config.
func TestHandlePatchConfig_MaestroLegacyBooleanNotHonoured(t *testing.T) {
	configPath := setupTestEnv(t)

	rec := patchMaestroConfig(t, configPath, `{"agents":{"list":[{"id":"alice","maestro":true}]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("config must still load: %v", err)
	}
	if cfg.AgentHasMaestro("alice") {
		t.Error("legacy boolean enabled Maestro")
	}
}

// TestHandlePatchConfig_MaestroMalformedRejected: a wrongly typed field is a
// validation error, not a silent default.
func TestHandlePatchConfig_MaestroMalformedRejected(t *testing.T) {
	configPath := setupTestEnv(t)

	rec := patchMaestroConfig(t, configPath, `{"agents":{"list":[{"id":"alice","maestro":{"enabled":true,"max_concurrent":"lots"}}]}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
