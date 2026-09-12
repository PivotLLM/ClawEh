package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// readyFixture writes a config with one resolvable CLI provider, one CLI
// provider pinned to a path that does not exist, one HTTP provider with a key
// and one without, and points PATH at a directory holding only the CLI binary.
func readyFixture(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{
		{Name: "Antigravity CLI", Protocol: "antigravity-cli"},
		{Name: "Claude CLI", Protocol: "claude-cli", Command: filepath.Join(dir, "gone")},
		{Name: "Keyed", Protocol: "openai-chat", BaseURL: "https://api.example/v1", APIKey: "sk-x"},
		{Name: "Keyless", Protocol: "openai-chat", BaseURL: "https://api.example/v1"},
	}
	cfg.Models = nil

	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	return NewHandler(path)
}

// providerReady is unit-tested directly, but nothing asserted that the answer
// reaches the wire. It has to: the Providers page draws its green dot and its
// Configured label from this field alone, so dropping it from the response
// leaves the Go tests green and every provider reading "Not configured" — and
// the browser suite would not catch that either, since it counts labels rather
// than which label.
func TestListProviders_CarriesReadinessOnTheWire(t *testing.T) {
	h := readyFixture(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	// Decode loosely: the point is what is in the JSON, not what the struct
	// would have filled in.
	var body struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Providers) != 4 {
		t.Fatalf("got %d providers", len(body.Providers))
	}

	byName := map[string]map[string]any{}
	for _, p := range body.Providers {
		name, _ := p["name"].(string)
		byName[name] = p
		if _, present := p["ready"]; !present {
			t.Errorf("provider %q has no ready field in the JSON", name)
		}
	}

	tests := []struct {
		name  string
		ready bool
		why   string
	}{
		{"Antigravity CLI", true, "a blank command resolves the catalogue binary on PATH"},
		{"Claude CLI", false, "the pinned path does not exist"},
		{"Keyed", true, "an HTTP provider with a key"},
		{"Keyless", false, "an HTTP provider without one"},
	}
	for _, tc := range tests {
		if got, _ := byName[tc.name]["ready"].(bool); got != tc.ready {
			t.Errorf("%s: ready = %v, want %v (%s)", tc.name, got, tc.ready, tc.why)
		}
	}

	// A blank command still has to name the binary it resolved, or the card
	// shows a green dot next to nothing.
	if got, _ := byName["Antigravity CLI"]["resolved_command"].(string); got == "" {
		t.Error("a resolvable CLI provider reports no resolved_command")
	}
	if _, present := byName["Claude CLI"]["resolved_command"]; present {
		t.Error("an unresolvable CLI provider invented a resolved_command")
	}
}

// The Status page's "Providers configured" figure and the Providers page's dots
// are meant to be the same rule applied twice. Counting every configured entry
// instead would report 24 on an install where 3 can actually be used.
func TestSystemStatus_CountsConfiguredProvidersNotAllOfThem(t *testing.T) {
	h := readyFixture(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Two of the four fixture providers are usable.
	if got.Providers != 2 {
		t.Errorf("providers = %d, want 2 of the 4 configured", got.Providers)
	}
}

// Models are counted as enabled for the same reason: an install carrying 40
// model definitions of which 3 can run is described by the 3.
func TestSystemStatus_CountsEnabledModelsNotAllOfThem(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{{Name: "P", Protocol: "openai-chat", BaseURL: "https://x/v1", APIKey: "k"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "on", Model: "m1", Provider: "P", Enabled: true},
		{ModelName: "off", Model: "m2", Provider: "P", Enabled: false},
		{ModelName: "off2", Model: "m3", Provider: "P", Enabled: false},
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(path)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/status", nil))
	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Models != 1 {
		t.Errorf("models = %d, want 1 enabled of 3", got.Models)
	}
}
