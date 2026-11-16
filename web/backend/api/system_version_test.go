package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/internal/testbinary"
)

func getNamesJSON(t *testing.T, mux *http.ServeMux, path string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET %s body %q: %v", path, rec.Body.String(), err)
	}
	return out
}

// TestVersion_CarriesProductNames pins the names the WebUI renders instead of
// holding the product name itself, and the version it always returned.
func TestVersion_CarriesProductNames(t *testing.T) {
	testbinary.Set(t, "alice-claw")
	h := &Handler{}
	mux := http.NewServeMux()
	h.registerVersionRoutes(mux)

	got := getNamesJSON(t, mux, "/api/system/version")
	want := map[string]any{
		"version":     app.Version(),
		"name":        app.Name(),
		"short_name":  app.ShortName(),
		"binary_name": "alice-claw",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

// TestAuthStatus_CarriesProductNames: the login page needs the names, and the
// binary name for its "run … admin" hint, before there is a session.
func TestAuthStatus_CarriesProductNames(t *testing.T) {
	testbinary.Set(t, "alice-claw")
	h := &Handler{}
	mux := http.NewServeMux()
	h.registerAuthRoutes(mux)

	got := getNamesJSON(t, mux, "/api/auth/status")
	for k, v := range map[string]any{
		"name":        app.Name(),
		"short_name":  app.ShortName(),
		"binary_name": "alice-claw",
	} {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}
