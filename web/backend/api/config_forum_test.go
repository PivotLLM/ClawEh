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

// TestHandlePatchConfig_ForumLimitsRoundTrip: the System page's forum.limits
// patch is saved and served back, a null clears a field to its default, and
// a negative value is refused.
func TestHandlePatchConfig_ForumLimitsRoundTrip(t *testing.T) {
	configPath := setupTestEnv(t)

	rec := patchMaestroConfig(t, configPath, `{"forum":{"limits":{"max_calls":300,"max_duration_seconds":3600,"call_timeout_seconds":900,"max_parallel_calls":4}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := config.ForumLimitsConfig{MaxCalls: 300, MaxDurationSeconds: 3600, CallTimeoutSeconds: 900, MaxParallelCalls: 4}
	if cfg.Forum.Limits != want {
		t.Fatalf("persisted forum.limits = %+v, want %+v", cfg.Forum.Limits, want)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var served struct {
		Forum struct {
			Limits config.ForumLimitsConfig `json:"limits"`
		} `json:"forum"`
	}
	if err = json.Unmarshal(get.Body.Bytes(), &served); err != nil {
		t.Fatalf("GET /api/config: %v (%s)", err, get.Body.String())
	}
	if served.Forum.Limits != want {
		t.Errorf("served forum.limits = %+v, want %+v", served.Forum.Limits, want)
	}

	rec = patchMaestroConfig(t, configPath, `{"forum":{"limits":{"max_calls":null}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if cfg, err = config.LoadConfig(configPath); err != nil {
		t.Fatal(err)
	}
	if cfg.Forum.Limits.MaxCalls != 0 || cfg.Forum.Limits.Effective().MaxCalls != config.DefaultForumMaxCalls {
		t.Errorf("after null max_calls = %+v, want it unset (default)", cfg.Forum.Limits)
	}

	rec = patchMaestroConfig(t, configPath, `{"forum":{"limits":{"max_parallel_calls":-1}}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "The forum maximum for max_parallel_calls must be 0 (the default) or more.") {
		t.Errorf("negative limit: status = %d, body=%s", rec.Code, rec.Body.String())
	}
}
