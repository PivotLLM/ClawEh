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

// GET /api/agents/mounts/ignored names each saved mount set aside for a
// reserved name, so the Agents page can mark it; a normal mount is not listed.
func TestHandleIgnoredMounts(t *testing.T) {
	configPath := setupTestEnv(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{ID: "alice", Mounts: []config.MountConfig{
		{Name: "Files", Path: t.TempDir()},
		{Name: "notes", Path: t.TempDir()},
	}})
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/mounts/ignored", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Ignored []ignoredMount `json:"ignored"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Ignored) != 1 || body.Ignored[0] != (ignoredMount{Agent: "alice", Mount: "Files"}) {
		t.Errorf("ignored = %+v, want [alice Files]", body.Ignored)
	}
}

// A WebUI save adding a mount with a reserved name is refused with the
// sentence naming the agent and the mount; a normal name is saved.
func TestPatchConfig_ReservedMountRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
	}{
		{"Tasks", http.StatusBadRequest},
		{"notes", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath := setupTestEnv(t)
			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			body := `{"agents":{"list":[{"id":"main","name":"Main","default":true,"mounts":[{"name":"` +
				tc.name + `","path":"` + t.TempDir() + `"}]}]}}`
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.code {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.code, rec.Body.String())
			}
			if tc.code != http.StatusOK && !anyContains(responseErrors(t, rec), `Main's mount "Tasks" uses a reserved name; choose another name.`) {
				t.Errorf("body = %q", rec.Body.String())
			}
		})
	}
}
