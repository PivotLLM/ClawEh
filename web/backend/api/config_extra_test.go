package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

func TestHandleGetConfig_ReturnsConfig(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var cfg config.Config
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
}

func TestHandleGetConfig_UnreadableConfigReturns500(t *testing.T) {
	// Use a directory as the config path — LoadConfig will fail to parse it
	dir := t.TempDir()
	h := NewHandler(dir) // dir is not a valid JSON file
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestHandleUpdateConfig_InvalidJSONReturns400(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleUpdateConfig_ValidationErrorReturns400(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// WebUI enabled without token should fail validation
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
		"agents": {"defaults": {"workspace": "~/.claw/workspace"}, "list": [{"id": "main", "name": "Main", "default": true}]},
		"providers": [{"name": "openai", "protocol": "openai-chat", "base_url": "https://api.openai.com/v1", "api_key": "k"}],
		"models": [{"model_name": "m", "model": "gpt-4o", "provider": "openai", "enabled": true}],
		"channels": {"webui": {"enabled": true, "token": ""}}
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body["status"] != "validation_error" {
		t.Fatalf("status = %q, want validation_error", body["status"])
	}
}

func TestHandlePatchConfig_PartialUpdate(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"gateway": {"port": 19000}
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Gateway.Port != 19000 {
		t.Fatalf("gateway.port = %d, want 19000", cfg.Gateway.Port)
	}
}

func TestHandlePatchConfig_InvalidJSONReturns400(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandlePatchConfig_ValidationFailureReturns400(t *testing.T) {
	configPath, cleanup := setupTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Patch in a gateway port that is out of range
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"gateway": {"port": 99999}
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandlePatchConfig_UnreadableConfigReturns500(t *testing.T) {
	// Use a directory as the config path — LoadConfig will fail to parse it
	dir := t.TempDir()
	h := NewHandler(dir) // dir is not a valid JSON file
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{"gateway":{"port":0}}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestValidateConfig_TelegramBotMissingToken(t *testing.T) {
	cfg := validConfigForValidation()
	cfg.Channels.Telegram = []config.TelegramBotConfig{{
		ID:      "bot1",
		Enabled: true,
		Token:   "",
	}}

	errs := validateConfig(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation errors for enabled telegram bot without token")
	}
	found := false
	for _, e := range errs {
		if containsStr(e, "bot1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("errors = %v, expected error mentioning bot1", errs)
	}
}

func TestValidateConfig_DiscordMissingToken(t *testing.T) {
	cfg := validConfigForValidation()
	cfg.Channels.Discord.Enabled = true
	cfg.Channels.Discord.Token = ""

	errs := validateConfig(cfg)
	if !anyContains(errs, "channels.discord.token") {
		t.Fatalf("errs = %v, want an error naming channels.discord.token", errs)
	}
}

func TestMergeMap_NullDeletesKey(t *testing.T) {
	dst := map[string]any{"a": "old", "b": "keep"}
	src := map[string]any{"a": nil}
	mergeMap(dst, src)

	if _, ok := dst["a"]; ok {
		t.Fatal("key 'a' should have been deleted by null patch")
	}
	if dst["b"] != "keep" {
		t.Fatalf("key 'b' = %v, want 'keep'", dst["b"])
	}
}

func TestMergeMap_RecursiveMerge(t *testing.T) {
	dst := map[string]any{
		"nested": map[string]any{"x": 1, "y": 2},
	}
	src := map[string]any{
		"nested": map[string]any{"x": 99},
	}
	mergeMap(dst, src)

	nested, ok := dst["nested"].(map[string]any)
	if !ok {
		t.Fatal("nested should remain a map")
	}
	if nested["x"] != 99 {
		t.Fatalf("nested.x = %v, want 99", nested["x"])
	}
	if nested["y"] != 2 {
		t.Fatalf("nested.y = %v, want 2 (should be preserved)", nested["y"])
	}
}

func TestMergeMap_OverwritesScalar(t *testing.T) {
	dst := map[string]any{"key": "old"}
	src := map[string]any{"key": "new"}
	mergeMap(dst, src)

	if dst["key"] != "new" {
		t.Fatalf("key = %v, want 'new'", dst["key"])
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

// validConfigForValidation returns a config that validateConfig accepts, so each
// test below can introduce exactly one defect and attribute the resulting error
// to it. Guarded by TestValidateConfig_BaselineIsValid.
func validConfigForValidation() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Models = []config.ModelConfig{{
		ModelName: "m",
		Model:     "gpt-4o",
		Provider:  "OpenAI", // must match a DefaultConfig provider name exactly
		Enabled:   true,
	}}
	return cfg
}

// TestValidateConfig_BaselineIsValid anchors the negative tests below. Without
// it a change that made validateConfig reject everything would leave them all
// passing for the wrong reason.
func TestValidateConfig_BaselineIsValid(t *testing.T) {
	if errs := validateConfig(validConfigForValidation()); len(errs) != 0 {
		t.Fatalf("baseline config should validate cleanly, got %v", errs)
	}
}

// TestValidateConfig_NoAgents covers the agents.list rule.
func TestValidateConfig_NoAgents(t *testing.T) {
	cfg := validConfigForValidation()
	cfg.Agents.List = nil

	errs := validateConfig(cfg)
	if !anyContains(errs, "agents.list") {
		t.Fatalf("errs = %v, want an error naming agents.list", errs)
	}
}

// TestValidateConfig_SurfacesModelErrors checks that validateConfig propagates
// ValidateModels. Before these tests existed this branch was exercised only by
// accident: the fixtures named the provider "openai" when DefaultConfig calls it
// "OpenAI", so every one of them tripped "provider not found" on the way past.
// Fixing the fixtures removed that accidental coverage, so the branch is now
// asserted on purpose.
func TestValidateConfig_SurfacesModelErrors(t *testing.T) {
	cfg := validConfigForValidation()
	cfg.Models[0].Provider = "no-such-provider"

	errs := validateConfig(cfg)
	if !anyContains(errs, "no-such-provider") {
		t.Fatalf("errs = %v, want an error naming the unknown provider", errs)
	}
}

// TestValidateConfig_SurfacesBindingErrors checks that validateConfig propagates
// ValidateBindings. ValidateBindings is covered by its own tests in the config
// package; what is asserted here is the wiring, which those cannot see. Two
// default bindings for one agent is the simplest way to trip it.
func TestValidateConfig_SurfacesBindingErrors(t *testing.T) {
	cfg := validConfigForValidation()
	cfg.Bindings = []config.AgentBinding{
		{AgentID: "alice", Match: config.BindingMatch{Channel: "telegram"}, Default: true, DeliverTo: "1"},
		{AgentID: "alice", Match: config.BindingMatch{Channel: "telegram"}, Default: true, DeliverTo: "2"},
	}

	errs := validateConfig(cfg)
	if !anyContains(errs, "more than one default binding") {
		t.Fatalf("errs = %v, want the duplicate-default binding error", errs)
	}
}

// TestValidateConfig_SurfacesCIDRErrors checks that validateConfig propagates
// config.ValidateAllowedCIDRs — again the wiring, not the validator itself.
func TestValidateConfig_SurfacesCIDRErrors(t *testing.T) {
	cfg := validConfigForValidation()
	cfg.Gateway.AllowedCIDRs = []string{"192.168.1.0/24", "not-a-cidr"}

	errs := validateConfig(cfg)
	if !anyContains(errs, "gateway.allowed_cidrs") {
		t.Fatalf("errs = %v, want an error naming gateway.allowed_cidrs", errs)
	}
}

// TestValidateConfig_MCPHostListen covers the mcp_host.listen rule, including
// that an empty value is skipped rather than rejected.
func TestValidateConfig_MCPHostListen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listen  string
		wantErr bool
	}{
		{"empty is skipped", "", false},
		{"missing port", "127.0.0.1", true},
		{"trailing colon", "127.0.0.1:", true},
		{"non-integer port", "127.0.0.1:abc", true},
		{"port above range", "127.0.0.1:70000", true},
		{"port zero", "127.0.0.1:0", true},
		{"valid", "127.0.0.1:5911", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfigForValidation()
			cfg.MCPHost.Listen = tc.listen

			errs := validateConfig(cfg)
			got := anyContains(errs, "mcp_host.listen")
			if got != tc.wantErr {
				t.Fatalf("listen=%q produced %v; want mcp_host.listen error = %v", tc.listen, errs, tc.wantErr)
			}
		})
	}
}

// TestValidateConfig_MCPHostEndpointPath covers the endpoint_path leading-slash
// rule, including that an empty value is skipped.
func TestValidateConfig_MCPHostEndpointPath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"empty is skipped", "", false},
		{"no leading slash", "mcp", true},
		{"valid", "/mcp", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfigForValidation()
			cfg.MCPHost.EndpointPath = tc.path

			errs := validateConfig(cfg)
			got := anyContains(errs, "endpoint_path")
			if got != tc.wantErr {
				t.Fatalf("endpoint_path=%q produced %v; want endpoint_path error = %v", tc.path, errs, tc.wantErr)
			}
		})
	}
}

// TestValidateListenAddr covers the helper directly, including the three error
// returns validateConfig only reaches through mcp_host.listen.
func TestValidateListenAddr(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		wantErr string // substring; empty means the input must be accepted
	}{
		{"host and port", "127.0.0.1:5911", ""},
		{"all interfaces", "0.0.0.0:18790", ""},
		{"hostname", "localhost:80", ""},
		{"port only, leading colon", ":5911", "host:port"},
		{"no colon", "127.0.0.1", "host:port"},
		{"trailing colon", "127.0.0.1:", "host:port"},
		{"empty", "", "host:port"},
		{"non-integer port", "127.0.0.1:port", "not an integer"},
		{"port zero", "127.0.0.1:0", "out of valid range"},
		{"port above 65535", "127.0.0.1:65536", "out of valid range"},
		{"negative port", "127.0.0.1:-1", "out of valid range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateListenAddr(tc.in)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("validateListenAddr(%q) = %v, want nil", tc.in, err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("validateListenAddr(%q) = nil, want an error containing %q", tc.in, tc.wantErr)
			case tc.wantErr != "" && !containsStr(err.Error(), tc.wantErr):
				t.Fatalf("validateListenAddr(%q) = %q, want it to contain %q", tc.in, err, tc.wantErr)
			}
		})
	}
}

// anyContains reports whether any error message contains sub.
func anyContains(errs []string, sub string) bool {
	for _, e := range errs {
		if containsStr(e, sub) {
			return true
		}
	}
	return false
}
