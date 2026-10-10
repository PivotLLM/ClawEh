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

// responseErrors decodes a JSON error answer and returns its errors list.
func responseErrors(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON: %s", ct, rec.Body.String())
	}
	var body struct {
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return body.Errors
}

// wantRefusal checks a 400 validation_error answer whose errors list holds
// exactly the given sentences.
func wantRefusal(t *testing.T, rec *httptest.ResponseRecorder, want ...string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Status != "validation_error" {
		t.Fatalf("body = %s, want a validation_error answer", rec.Body.String())
	}
	got := responseErrors(t, rec)
	if len(got) != len(want) {
		t.Fatalf("errors = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("errors[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// humanFixture adds a human provider, the model "Bob (human)" on it and the
// agent bob using it to the test config.
func humanFixture(t *testing.T, configPath string) {
	t.Helper()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers = append(cfg.Providers, config.Provider{Name: "People", Protocol: config.HumanProtocol})
	cfg.Models = append(cfg.Models, config.ModelConfig{ModelName: "Bob (human)", Model: "bob", Provider: "People", Enabled: true})
	cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{ID: "bob", Models: []string{"Bob (human)"}})
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
}

func serveSave(t *testing.T, configPath, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	return rec
}

// Every kind of refusal config.Store.Update makes reaches the client through
// PATCH /api/config as 400 JSON, one sentence per refusal.
func TestPatchConfig_StoreRefusalsAreJSON(t *testing.T) {
	for _, tc := range []struct {
		name  string
		human bool
		patch string
		want  []string
	}{
		{
			name:  "dangling model",
			patch: `{"agents":{"list":[{"id":"main","name":"Main","default":true,"models":["Ghost"]}]}}`,
			want:  []string{`agents.list[main].models: model "Ghost" does not exist`},
		},
		{
			name:  "human agent",
			human: true,
			patch: `{"summarization":{"models":["Bob (human)"]}}`,
			want:  []string{"Bob (human) represents a person and can't be a summarization model."},
		},
		{
			name: "reserved mounts",
			patch: `{"agents":{"list":[{"id":"main","name":"Main","default":true,"mounts":[` +
				`{"name":"Tasks","path":"/tmp/a"},{"name":"files","path":"/tmp/b"}]}]}}`,
			want: []string{
				`Main's mount "Tasks" uses a reserved name; choose another name.`,
				`Main's mount "files" uses a reserved name; choose another name.`,
			},
		},
		{
			name:  "agent id",
			patch: `{"agents":{"list":[{"id":"main","name":"Main","default":true,"subagents":{"allow_agents":["Alice"]}}]}}`,
			want:  []string{`Agent id "Alice" in Main's subagents.allow_agents may use only lower-case letters, digits, - and _; use "alice".`},
		},
		{
			name:  "listener",
			patch: `{"mcp_host":{"listen":"0.0.0.0:5911"}}`,
			want:  []string{`mcp_host.listen "0.0.0.0:5911": the MCP host is plain HTTP and must listen on a loopback address (127.0.0.1 or ::1)`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath := setupTestEnv(t)
			if tc.human {
				humanFixture(t, configPath)
			}
			rec := serveSave(t, configPath, http.MethodPatch, "/api/config", tc.patch)
			wantRefusal(t, rec, tc.want...)
		})
	}
}

// A refusal on a save path other than PATCH /api/config is answered the same
// way.
func TestSaveEndpoints_StoreRefusalsAreJSON(t *testing.T) {
	t.Run("default model", func(t *testing.T) {
		configPath := setupTestEnv(t)
		humanFixture(t, configPath)
		rec := serveSave(t, configPath, http.MethodPost, "/api/models/default", `{"model_name":"Bob (human)"}`)
		wantRefusal(t, rec, "Bob (human) represents a person and can't be a default model.")
	})
	t.Run("device settings", func(t *testing.T) {
		configPath := setupTestEnv(t)
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Channels.Device.AutoApprove = true
		if err := config.SaveConfig(configPath, cfg); err != nil {
			t.Fatal(err)
		}
		rec := serveSave(t, configPath, http.MethodPost, "/api/devices/settings", `{"listen_lan":true,"enabled":true}`)
		wantRefusal(t, rec, "channels.device.auto_approve must be off when the device listener is on a network address")
	})
}

// An error raised inside a save that already knows its status keeps that
// status and is JSON too.
func TestSaveEndpoints_OtherErrorsAreJSON(t *testing.T) {
	configPath := setupTestEnv(t)
	rec := serveSave(t, configPath, http.MethodDelete, "/api/models/7", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if got := responseErrors(t, rec); len(got) != 1 || !strings.Contains(got[0], "out of range") {
		t.Errorf("errors = %q", got)
	}
}
