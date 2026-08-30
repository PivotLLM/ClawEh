// ClawEh
// License: MIT

package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// patchConfig applies a merge patch through the real handler and returns the
// reloaded config, failing the test on a non-200.
func patchConfig(t *testing.T, configPath, body string) *config.Config {
	t.Helper()
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

// TestPatchCompression_SetsNestedValues is the plain path: the WebUI writes the
// nested compaction block and it survives the merge-patch round trip.
func TestPatchCompression_SetsNestedValues(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	cfg := patchConfig(t, configPath, `{
	  "agents": {"defaults": {"compression": {
	    "trigger": {"days": 7, "message_count": 100},
	    "retain":  {"max_age_days": 5, "max_tokens": 60000}
	  }}}
	}`)

	c := cfg.Agents.Defaults.Compression
	if c == nil || c.Trigger == nil || c.Trigger.Days == nil || *c.Trigger.Days != 7 {
		t.Fatalf("trigger.days not persisted: %+v", c)
	}
	if c.Retain == nil || c.Retain.MaxAgeDays == nil || *c.Retain.MaxAgeDays != 5 {
		t.Errorf("retain.max_age_days not persisted: %+v", c.Retain)
	}
	if c.Retain.MaxTokens == nil || *c.Retain.MaxTokens != 60000 {
		t.Errorf("retain.max_tokens not persisted: %+v", c.Retain)
	}
}

// TestPatchCompression_ExplicitZeroDisables covers the setting that only became
// expressible once these fields were pointers: 0 turns a trigger off, and must
// survive as a real 0 rather than being read back as "unset".
func TestPatchCompression_ExplicitZeroDisables(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	patchConfig(t, configPath, `{"agents":{"defaults":{"compression":{"trigger":{"days":7}}}}}`)
	cfg := patchConfig(t, configPath, `{"agents":{"defaults":{"compression":{"trigger":{"days":0}}}}}`)

	c := cfg.Agents.Defaults.Compression
	if c == nil || c.Trigger == nil || c.Trigger.Days == nil {
		t.Fatalf("trigger.days lost entirely; an explicit 0 must persist: %+v", c)
	}
	if *c.Trigger.Days != 0 {
		t.Errorf("trigger.days = %d, want 0 (disabled)", *c.Trigger.Days)
	}
}

// TestPatchCompression_NullClearsBackToDefault is the reason the WebUI sends
// null rather than omitting a blank field. PATCH is JSON Merge Patch: an omitted
// key means "leave the existing value alone", so clearing a box in the UI would
// silently keep the old value. Null deletes the key, and the backend default
// applies again.
func TestPatchCompression_NullClearsBackToDefault(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	patchConfig(t, configPath, `{"agents":{"defaults":{"compression":{
	  "trigger": {"days": 7, "message_count": 100}
	}}}}`)

	cfg := patchConfig(t, configPath, `{"agents":{"defaults":{"compression":{
	  "trigger": {"days": null, "message_count": 100}
	}}}}`)

	c := cfg.Agents.Defaults.Compression
	if c == nil || c.Trigger == nil {
		t.Fatalf("compression block lost: %+v", c)
	}
	if c.Trigger.Days != nil {
		t.Errorf("trigger.days = %v, want nil (cleared back to the default)", *c.Trigger.Days)
	}
	if c.Trigger.MessageCount == nil || *c.Trigger.MessageCount != 100 {
		t.Errorf("clearing one field must not disturb its siblings: %+v", c.Trigger)
	}
}

// TestPatchCompression_UntouchedSaveKeepsDefaults is the regression this whole
// exchange is about. The WebUI sends null for every unset compaction field, so
// opening the config page and saving it without typing anything must leave the
// policy exactly as it was — NOT write explicit zeroes that disable the count
// trigger, the age trigger and the tail budget.
func TestPatchCompression_UntouchedSaveKeepsDefaults(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	// Exactly what the form emits when every compaction box is blank.
	cfg := patchConfig(t, configPath, `{"agents":{"defaults":{"compression":{
	  "trigger": {"normal_percent": null, "safety_percent": null, "min_percent": null,
	              "message_count": null, "days": null},
	  "retain":  {"token_percent": null, "min_messages": null,
	              "max_age_days": null, "max_tokens": null}
	}}}}`)

	got := (&config.AgentConfig{}).EffectiveCompression(cfg.Agents.Defaults.Compression)
	if got.Trigger != nil {
		if got.Trigger.Days != nil {
			t.Errorf("a blank save disabled the age trigger (days=%d)", *got.Trigger.Days)
		}
		if got.Trigger.MessageCount != nil {
			t.Errorf("a blank save disabled the count trigger (message_count=%d)", *got.Trigger.MessageCount)
		}
	}
	if got.Retain != nil {
		if got.Retain.TokenPercent != nil {
			t.Errorf("a blank save pinned the tail budget (token_percent=%d)", *got.Retain.TokenPercent)
		}
		if got.Retain.MaxAgeDays != nil {
			t.Errorf("a blank save disabled the age cap (max_age_days=%d)", *got.Retain.MaxAgeDays)
		}
	}
}

// TestPatchCompression_SaveRewritesLegacyKeys verifies the migration is
// self-healing through the UI: the save handler persists the decoded struct, in
// which the legacy fields have already been cleared by migration, so the first
// save rewrites config.json in the nested form.
func TestPatchCompression_SaveRewritesLegacyKeys(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	// Seed a pre-split config, then save something unrelated.
	patchConfig(t, configPath, `{"agents":{"defaults":{"compress_min_percent":25}}}`)
	cfg := patchConfig(t, configPath, `{"gateway":{"port":19001}}`)

	if cfg.Agents.Defaults.CompressMinPercent != nil {
		t.Errorf("legacy compress_min_percent still set after a save: %d",
			*cfg.Agents.Defaults.CompressMinPercent)
	}
	c := cfg.Agents.Defaults.Compression
	if c == nil || c.Trigger == nil || c.Trigger.MinPercent == nil || *c.Trigger.MinPercent != 25 {
		t.Fatalf("the migrated value was lost instead of being rewritten nested: %+v", c)
	}
}

// TestProviderRequireReasoningContent_RoundTrips covers the read-back path for
// the flag: writes decode straight into config.Provider, but the list response
// is a separate DTO, so a field missing there would silently reset the checkbox
// every time an operator opened the edit sheet.
func TestProviderRequireReasoningContent_RoundTrips(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	body := `{"name":"ds","protocol":"openai-chat","base_url":"https://api.deepseek.com",` +
		`"api_key":"k","require_reasoning_content":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/providers", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create provider: status %d, body=%s", rec.Code, rec.Body.String())
	}

	// Persisted?
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	var found *config.Provider
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == "ds" {
			found = &cfg.Providers[i]
		}
	}
	if found == nil {
		t.Fatal("provider not persisted")
	}
	if !found.RequireReasoningContent {
		t.Error("require_reasoning_content did not persist")
	}

	// Readable back through the list endpoint?
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list providers: status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"require_reasoning_content":true`) {
		t.Errorf("flag missing from the list response — the edit sheet would reset it:\n%s", rec.Body.String())
	}
}
