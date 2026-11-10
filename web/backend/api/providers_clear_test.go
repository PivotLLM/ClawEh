// ClawEh
// License: MIT

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// putProvider sends PUT /api/providers/<index> with body and returns the
// response.
func putProvider(t *testing.T, mux *http.ServeMux, index, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/providers/"+index, strings.NewReader(body)))
	return rec
}

// On an update an omitted base_url keeps the stored one and an explicit
// empty one clears it; a protocol that requires it still refuses the clear.
func TestUpdateProvider_EmptyBaseURLClears(t *testing.T) {
	p, _, mux := refFixture(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/providers",
		strings.NewReader(`{"name": "Speech", "protocol": "gemini", "base_url": "https://speech.example/v1", "api_key": "sk-speech-0123456789"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/providers = %d: %s", rec.Code, rec.Body.String())
	}
	baseURL := func() string {
		t.Helper()
		cfg, err := config.LoadConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Providers[1].BaseURL
	}

	if rec := putProvider(t, mux, "1", `{"name": "Speech", "protocol": "gemini"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT without base_url = %d: %s", rec.Code, rec.Body.String())
	}
	if got := baseURL(); got != "https://speech.example/v1" {
		t.Fatalf("omitted base_url: stored %q, want it kept", got)
	}

	if rec := putProvider(t, mux, "1", `{"name": "Speech", "protocol": "gemini", "base_url": ""}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT with empty base_url = %d: %s", rec.Code, rec.Body.String())
	}
	if got := baseURL(); got != "" {
		t.Fatalf("empty base_url: stored %q, want it cleared", got)
	}

	rec = putProvider(t, mux, "0", `{"name": "openai", "protocol": "openai-chat", "base_url": ""}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("clearing a required base_url = %d: %s, want 400", rec.Code, rec.Body.String())
	}
}
