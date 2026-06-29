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

func TestSetupStatus_FreshInstallNeedsSetup(t *testing.T) {
	// Seeded default catalog: pristine, every model disabled (the CLI models that
	// always "configure" must not count because they aren't enabled).
	got := setupStatusFor(t, config.DefaultConfig(), true)
	if !got.Pristine || got.HasUsableModel || !got.NeedsSetup {
		t.Fatalf("fresh install: got %+v, want pristine=true usable=false needsSetup=true", got)
	}
}

func TestSetupStatus_SavedConfigNoSetup(t *testing.T) {
	// A save clears the pristine marker, so the wizard isn't forced even with no
	// usable model.
	got := setupStatusFor(t, config.DefaultConfig(), false)
	if got.Pristine || got.NeedsSetup {
		t.Fatalf("saved config: got %+v, want pristine=false needsSetup=false", got)
	}
}

func TestSetupStatus_PristineWithUsableModelNoSetup(t *testing.T) {
	// Hand-edited but still pristine: an enabled model with a keyed provider is
	// usable, so the "no usable model" guard keeps the user out of the wizard.
	cfg := config.DefaultConfig()
	enabled := false
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == "OpenAI" {
			cfg.Providers[i].APIKey = "sk-test"
		}
	}
	for i := range cfg.Models {
		if cfg.Models[i].Provider == "OpenAI" {
			cfg.Models[i].Enabled = true
			enabled = true
			break
		}
	}
	if !enabled {
		t.Fatal("fixture: no OpenAI model in the seeded catalog")
	}

	got := setupStatusFor(t, cfg, true)
	if !got.Pristine || !got.HasUsableModel || got.NeedsSetup {
		t.Fatalf("pristine+usable: got %+v, want pristine=true usable=true needsSetup=false", got)
	}
}
