// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func getAlerts(t *testing.T, h *Handler) map[string]any {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/gateway/alerts?lines=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// TestGatewayAlerts: disabled reports an error, a missing file is an empty
// list, and an existing file is tailed like the gateway log.
func TestGatewayAlerts(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	if out := getAlerts(t, h); out["error"] != "alerting is disabled" {
		t.Errorf("no path: %v", out)
	}

	path := filepath.Join(t.TempDir(), "alerts.txt")
	h.SetAlertsPath(path)
	if out := getAlerts(t, h); out["error"] != nil || out["count"] != float64(0) {
		t.Errorf("missing file: %v", out)
	}

	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := getAlerts(t, h)
	logs, ok := out["logs"].([]any)
	if !ok || len(logs) != 2 || logs[0] != "two" || logs[1] != "three" {
		t.Errorf("tail = %v", out)
	}
}
