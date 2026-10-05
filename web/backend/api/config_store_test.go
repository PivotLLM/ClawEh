package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

const (
	testRefEnv   = "CLAW_TEST_REF_API_KEY"
	testRefValue = "sk-resolved-from-env-0123456789"
)

// refFixture writes a config whose provider key is an env: reference (and a
// telegram bot with a literal token, for contrast) and serves it.
func refFixture(t *testing.T) (string, *Handler, *http.ServeMux) {
	t.Helper()
	t.Setenv(testRefEnv, testRefValue)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CLAW_HOME", filepath.Join(dir, ".claw"))
	p := filepath.Join(dir, "config.json")
	body := `{
		"agents": {"defaults": {"models": ["m"]}, "list": [{"id": "main", "name": "Main", "default": true}]},
		"providers": [{"name": "openai", "protocol": "openai-chat", "base_url": "https://api.openai.com/v1", "api_key": "env:` + testRefEnv + `"}],
		"models": [{"model_name": "m", "model": "gpt-4o", "provider": "openai", "enabled": true}],
		"channels": {"telegram": [{"id": "b1", "enabled": true, "token": "1234567:literal-telegram-token-xyz"}]}
	}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(p)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return p, h, mux
}

func diskProviderKey(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Providers []struct {
			APIKey string `json:"api_key"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("config.json is not JSON: %v", err)
	}
	if len(doc.Providers) == 0 {
		t.Fatal("no providers on disk")
	}
	return doc.Providers[0].APIKey
}

// objectAt returns the JSON object at path in doc, failing the test otherwise.
func objectAt(t *testing.T, doc any, path ...any) map[string]any {
	t.Helper()
	m, ok := pathValue(t, doc, path...).(map[string]any)
	if !ok {
		t.Fatalf("%v is not an object", path)
	}
	return m
}

func TestGetConfig_ShowsSecretReferenceNotValue(t *testing.T) {
	_, _, mux := refFixture(t)
	body := getConfigBody(t, mux)
	if !strings.Contains(body, `"api_key":"env:`+testRefEnv+`"`) {
		t.Fatalf("GET /api/config must show the reference as written, got: %s", body)
	}
	if strings.Contains(body, testRefValue) {
		t.Fatal("GET /api/config leaked the resolved value")
	}
	if strings.Contains(body, "literal-telegram-token") {
		t.Fatal("GET /api/config leaked a literal token")
	}
}

func TestPutConfig_RoundTripKeepsSecretReference(t *testing.T) {
	p, h, mux := refFixture(t)
	body := getConfigBody(t, mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/config = %d: %s", rec.Code, rec.Body.String())
	}

	if got := diskProviderKey(t, p); got != "env:"+testRefEnv {
		t.Fatalf("api_key on disk after PUT = %q, want the reference", got)
	}
	cfg, err := h.currentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers[0].APIKey; got != testRefValue {
		t.Fatalf("live api_key after PUT = %q, want the resolved value", got)
	}
	// The literal token echoed back masked was restored, not destroyed.
	if got := cfg.Channels.Telegram[0].Token; got != "1234567:literal-telegram-token-xyz" {
		t.Fatalf("telegram token after PUT = %q", got)
	}
	// And a second GET still shows the reference.
	if !strings.Contains(getConfigBody(t, mux), `"api_key":"env:`+testRefEnv+`"`) {
		t.Fatal("reference lost after the round trip")
	}
}

func TestPatchConfig_KeepsSecretReferenceAndAcceptsNewOne(t *testing.T) {
	t.Setenv("CLAW_TEST_TG_TOKEN", "9999:tg-from-env")
	p, h, mux := refFixture(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config",
		strings.NewReader(`{"channels": {"telegram": [{"id": "b1", "enabled": true, "token": "env:CLAW_TEST_TG_TOKEN"}]}}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/config = %d: %s", rec.Code, rec.Body.String())
	}

	if got := diskProviderKey(t, p); got != "env:"+testRefEnv {
		t.Fatalf("provider api_key on disk after PATCH = %q, want the reference kept", got)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"env:CLAW_TEST_TG_TOKEN"`) || strings.Contains(string(raw), "tg-from-env") {
		t.Fatalf("new reference not preserved on disk:\n%s", raw)
	}
	cfg, err := h.currentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Channels.Telegram[0].Token != "9999:tg-from-env" {
		t.Fatalf("live telegram token = %q, want the resolved value", cfg.Channels.Telegram[0].Token)
	}
}

func TestPutConfig_NewLiteralReplacesReference(t *testing.T) {
	p, h, mux := refFixture(t)
	var doc map[string]any
	if err := json.Unmarshal([]byte(getConfigBody(t, mux)), &doc); err != nil {
		t.Fatal(err)
	}
	objectAt(t, doc, "providers", 0)["api_key"] = "sk-typed-in-literal-0123456789"
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	if got := diskProviderKey(t, p); got != "sk-typed-in-literal-0123456789" {
		t.Fatalf("api_key on disk = %q, want the operator's literal", got)
	}
	cfg, err := h.currentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[0].APIKey != "sk-typed-in-literal-0123456789" {
		t.Fatal("live config did not take the literal")
	}
}

func TestPutConfig_MissingEnvReferenceIs400(t *testing.T) {
	if err := os.Unsetenv("CLAW_TEST_NOT_SET_ANYWHERE"); err != nil {
		t.Fatal(err)
	}
	p, _, mux := refFixture(t)
	var doc map[string]any
	if err := json.Unmarshal([]byte(getConfigBody(t, mux)), &doc); err != nil {
		t.Fatal(err)
	}
	objectAt(t, doc, "providers", 0)["api_key"] = "env:CLAW_TEST_NOT_SET_ANYWHERE"
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "CLAW_TEST_NOT_SET_ANYWHERE") {
		t.Fatalf("PUT = %d %s, want 400 naming the variable", rec.Code, rec.Body.String())
	}
	if got := diskProviderKey(t, p); got != "env:"+testRefEnv {
		t.Fatalf("disk changed on a rejected PUT: %q", got)
	}
}

func TestUpdateProvider_MaskedKeyKeepsReferenceOnDisk(t *testing.T) {
	p, _, mux := refFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/providers/0",
		strings.NewReader(`{"name": "openai", "protocol": "openai-chat", "base_url": "https://example.test/v1", "api_key": "sk-****6789"}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/providers/0 = %d: %s", rec.Code, rec.Body.String())
	}
	if got := diskProviderKey(t, p); got != "env:"+testRefEnv {
		t.Fatalf("api_key on disk = %q, want the reference kept through a masked edit", got)
	}
}

func TestPutConfig_RejectsWhatLoadConfigRefuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(doc map[string]any)
		want   string
	}{
		{"tls cert without key", func(doc map[string]any) {
			objectAt(t, doc, "gateway")["tls"] = map[string]any{"cert_file": "/etc/claw/cert.pem"}
		}, "gateway.tls.key_file"},
		{"mcp host off-box", func(doc map[string]any) {
			doc["mcp_host"] = map[string]any{"listen": "0.0.0.0:5911"}
		}, "loopback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _, mux := refFixture(t)
			before, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err = json.Unmarshal([]byte(getConfigBody(t, mux)), &doc); err != nil {
				t.Fatal(err)
			}
			tc.mutate(doc)
			body, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("PUT = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Status string   `json:"status"`
				Errors []string `json:"errors"`
			}
			if err = json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Status != "validation_error" {
				t.Fatalf("body = %s, want a validation_error response", rec.Body.String())
			}
			if !strings.Contains(strings.Join(resp.Errors, "\n"), tc.want) {
				t.Fatalf("errors %v do not mention %q", resp.Errors, tc.want)
			}
			after, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("a rejected PUT changed config.json")
			}
		})
	}
}

// TestHandler_SharesInjectedStore is the one-in-memory-config guarantee: a
// handler built on the gateway's store sees the gateway's changes without
// touching the file, and the gateway sees the handler's.
func TestHandler_SharesInjectedStore(t *testing.T) {
	p, _, _ := refFixture(t)
	store, err := config.NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithStore(store)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Gateway-side change, no file re-read by the handler.
	if err := store.Update(func(c *config.Config) error { c.Providers[0].Name = "renamed-by-gateway"; return nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(getConfigBody(t, mux), `"name":"renamed-by-gateway"`) {
		t.Fatal("handler did not see the store's change")
	}

	// Handler-side change, visible to the gateway.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/providers/0", strings.NewReader(`{"base_url": "https://from-api.test/v1"}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/providers/0 = %d: %s", rec.Code, rec.Body.String())
	}
	if got := store.Current().Providers[0].BaseURL; got != "https://from-api.test/v1" {
		t.Fatalf("store did not see the handler's change: %q", got)
	}
}

// TestHandler_ReadsLiveConfigNotFile pins the change from per-request file
// loads: an edit made behind the store's back is not visible until Reload.
func TestHandler_ReadsLiveConfigNotFile(t *testing.T) {
	p, h, mux := refFixture(t)
	if !strings.Contains(getConfigBody(t, mux), `"name":"openai"`) {
		t.Fatal("fixture not served")
	}
	cfg, err := config.LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers[0].Name = "edited-on-disk"
	if err = config.SaveConfig(p, cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(getConfigBody(t, mux), "edited-on-disk") {
		t.Fatal("handler re-read the file instead of serving the live config")
	}
	st, err := h.configStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Reload(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(getConfigBody(t, mux), "edited-on-disk") {
		t.Fatal("Reload did not pick up the file")
	}
}
