package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func setupStatusFor(t *testing.T, cfg *config.Config, seed bool) setupStatusResponse {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	var err error
	if seed {
		err = config.SeedDefaultConfig(path, cfg) // keeps the pristine marker
	} else {
		err = config.SaveConfig(path, cfg) // clears the pristine marker
	}
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	h := NewHandler(path)
	rec := httptest.NewRecorder()
	h.handleSetupStatus(rec, httptest.NewRequest(http.MethodGet, "/api/system/setup-status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got setupStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// withUsableOpenAIModel enables an OpenAI seeded model with a key so the config
// has exactly one usable model.
func withUsableOpenAIModel(t *testing.T, cfg *config.Config) *config.Config {
	t.Helper()
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == "OpenAI" {
			cfg.Providers[i].APIKey = "sk-test"
		}
	}
	for i := range cfg.Models {
		if cfg.Models[i].Provider == "OpenAI" {
			cfg.Models[i].Enabled = true
			return cfg
		}
	}
	t.Fatal("fixture: no OpenAI model in the seeded catalog")
	return cfg
}

func TestSetupStatus_FreshInstallNeedsSetup(t *testing.T) {
	// Seeded default catalog: every model disabled (the CLI models that always
	// "configure" must not count because they aren't enabled) → setup needed.
	got := setupStatusFor(t, config.DefaultConfig(), true)
	if got.HasUsableModel || !got.NeedsSetup {
		t.Fatalf("fresh install: got %+v, want usable=false needsSetup=true", got)
	}
}

func TestSetupStatus_NoUsableModelNeedsSetup_EvenIfSaved(t *testing.T) {
	// A save clears "pristine", but with no usable model the user still can't use
	// the app — so the wizard must still be offered. (Regression: editing an
	// unrelated setting like the bind address must not suppress onboarding.)
	got := setupStatusFor(t, config.DefaultConfig(), false)
	if got.HasUsableModel || !got.NeedsSetup {
		t.Fatalf("saved, no usable model: got %+v, want usable=false needsSetup=true", got)
	}
}

func TestSetupStatus_UsableModelNoSetup(t *testing.T) {
	// An enabled model with a keyed provider is usable → no wizard, whether or not
	// the config was saved.
	for _, seed := range []bool{true, false} {
		got := setupStatusFor(t, withUsableOpenAIModel(t, config.DefaultConfig()), seed)
		if !got.HasUsableModel || got.NeedsSetup {
			t.Fatalf("usable model (seed=%v): got %+v, want usable=true needsSetup=false", seed, got)
		}
	}
}
