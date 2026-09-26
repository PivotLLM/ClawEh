package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// storeFixture writes a minimal config into a private CLAW_HOME, opens the
// gateway's store on it and builds the merged WebUI server on that store, the
// way gatewayCmd and setupAndStartServices do.
func storeFixture(t *testing.T) (*config.Store, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	home := filepath.Join(dir, ".claw")
	t.Setenv("CLAW_HOME", home)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.json")
	body := `{
		"agents": {"list": [{"id": "main", "name": "Main", "default": true}]},
		"logging": {"level": "info"}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(path)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	cfg, err := runtimeConfig(store.Current())
	if err != nil {
		t.Fatalf("runtimeConfig: %v", err)
	}
	return store, buildMergedMux(newMergedWebServer(store, cfg))
}

// TestConfigStore_APISaveIsVisibleToGateway: a config saved through the WebUI
// API (PUT /api/config, i.e. store.Update) is what the gateway's next
// store.Current() returns, and an external write to the file is picked up by
// Reload, the way the config watcher does.
func TestConfigStore_APISaveIsVisibleToGateway(t *testing.T) {
	store, mux := storeFixture(t)
	if got := store.Current().Logging.Level; got != "info" {
		t.Fatalf("initial logging.level = %q, want info", got)
	}

	// Read the masked config back the way the WebUI does, change one field
	// and write it through the API.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config = %d: %s", rec.Code, rec.Body.String())
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("GET /api/config body: %v", err)
	}
	var logging map[string]any
	if err := json.Unmarshal(doc["logging"], &logging); err != nil {
		t.Fatalf("logging section: %v", err)
	}
	logging["level"] = "debug"
	raw, err := json.Marshal(logging)
	if err != nil {
		t.Fatal(err)
	}
	doc["logging"] = raw
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/config = %d: %s", rec.Code, rec.Body.String())
	}

	if got := store.Current().Logging.Level; got != "debug" {
		t.Fatalf("store.Current().Logging.Level after the API save = %q, want debug (the API wrote to a different store)", got)
	}
	onDisk, err := config.LoadConfig(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Logging.Level != "debug" {
		t.Fatalf("on disk logging.level = %q, want debug", onDisk.Logging.Level)
	}

	// Something else (claw CLI, an editor) writes the file: Reload picks it up
	// and the runtime copy is a clone, not the shared snapshot.
	external, err := store.Current().Clone()
	if err != nil {
		t.Fatal(err)
	}
	external.Logging.Level = "warn"
	if err = config.SaveConfig(store.Path(), external); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if got := store.Current().Logging.Level; got != "debug" {
		t.Fatalf("store changed without Reload: logging.level = %q", got)
	}
	live, err := store.Reload()
	if err != nil {
		t.Fatalf("store.Reload: %v", err)
	}
	if live.Logging.Level != "warn" || store.Current().Logging.Level != "warn" {
		t.Fatalf("after Reload: returned %q, current %q, want warn", live.Logging.Level, store.Current().Logging.Level)
	}
	runtime, err := runtimeConfig(store.Current())
	if err != nil {
		t.Fatal(err)
	}
	runtime.Logging.Level = "error"
	if got := store.Current().Logging.Level; got != "warn" {
		t.Fatalf("mutating the runtime copy changed the store: %q", got)
	}
}
