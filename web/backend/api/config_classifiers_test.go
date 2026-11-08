// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// TestHandlePatchConfig_ClassifiersRoundTrip: the System page's classifiers
// patch is saved and served back, a null clears a field to its default, and
// a negative value is refused with the store's sentence.
func TestHandlePatchConfig_ClassifiersRoundTrip(t *testing.T) {
	configPath := setupTestEnv(t)

	rec := patchMaestroConfig(t, configPath, `{"classifiers":{"processed_days":10,"limits":{"max_rules":50,"max_prompt_chars":1000,"max_item_chars":2000,"max_items_per_run":20,"max_parallel":2,"max_activity_hours":48,"min_every_seconds":60}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := config.ClassifiersConfig{ProcessedDays: 10, Limits: config.ClassifierLimitsConfig{
		MaxRules: 50, MaxPromptChars: 1000, MaxItemChars: 2000, MaxItemsPerRun: 20,
		MaxParallel: 2, MaxActivityHours: 48, MinEverySeconds: 60,
	}}
	if cfg.Classifiers != want {
		t.Fatalf("persisted classifiers = %+v, want %+v", cfg.Classifiers, want)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var served struct {
		Classifiers config.ClassifiersConfig `json:"classifiers"`
	}
	if err = json.Unmarshal(get.Body.Bytes(), &served); err != nil {
		t.Fatalf("GET /api/config: %v (%s)", err, get.Body.String())
	}
	if served.Classifiers != want {
		t.Errorf("served classifiers = %+v, want %+v", served.Classifiers, want)
	}

	rec = patchMaestroConfig(t, configPath, `{"classifiers":{"processed_days":null,"limits":{"min_every_seconds":null}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if cfg, err = config.LoadConfig(configPath); err != nil {
		t.Fatal(err)
	}
	if cfg.Classifiers.ProcessedDays != 0 || cfg.Classifiers.Limits.MinEverySeconds != 0 ||
		cfg.Classifiers.Effective().Limits.MinEverySeconds != config.DefaultClassifierMinEverySeconds {
		t.Errorf("after nulls = %+v, want processed_days and min_every_seconds unset (default)", cfg.Classifiers)
	}

	rec = patchMaestroConfig(t, configPath, `{"classifiers":{"limits":{"min_every_seconds":-1}}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "The classifier minimum for min_every_seconds must be 0 (the default) or more.") {
		t.Errorf("negative minimum: status = %d, body=%s", rec.Code, rec.Body.String())
	}
}
