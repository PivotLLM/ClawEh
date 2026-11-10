// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// GET /api/agents/allowed-folders/ignored names each saved allowed folder the
// file tools skip, with why, so the Agents page can mark it; a usable folder
// is not listed.
func TestHandleIgnoredAllowedFolders(t *testing.T) {
	configPath := setupTestEnv(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{ID: "alice", AllowedFolders: []config.AllowedFolderConfig{
		{Path: t.TempDir()}, {Path: "notes"}, {Path: missing},
	}})
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/allowed-folders/ignored", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Ignored []ignoredAllowedFolder `json:"ignored"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := []ignoredAllowedFolder{
		{Agent: "alice", Path: "notes", Reason: config.FolderNotAbsolute},
		{Agent: "alice", Path: missing, Reason: config.FolderNotFound},
	}
	if len(body.Ignored) != 2 || body.Ignored[0] != want[0] || body.Ignored[1] != want[1] {
		t.Errorf("ignored = %+v, want %+v", body.Ignored, want)
	}
}
