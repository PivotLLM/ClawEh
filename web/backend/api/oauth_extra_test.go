package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/auth"
)

func TestHandleListOAuthProviders_ReturnsProviders(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/providers", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Providers []oauthProviderStatus `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Providers) == 0 {
		t.Fatal("providers should not be empty")
	}

	names := make(map[string]struct{}, len(resp.Providers))
	for _, p := range resp.Providers {
		names[p.Provider] = struct{}{}
	}
	for _, expected := range []string{oauthProviderAnthropic} {
		if _, ok := names[expected]; !ok {
			t.Fatalf("provider %q not found in response", expected)
		}
	}
}

func TestHandleListOAuthProviders_ShowsConnectedStatus(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	if err := auth.SetCredential(oauthProviderAnthropic, &auth.AuthCredential{
		AccessToken: "test-token",
		Provider:    oauthProviderAnthropic,
		AuthMethod:  "token",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/providers", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Providers []oauthProviderStatus `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	for _, p := range resp.Providers {
		if p.Provider == oauthProviderAnthropic {
			if !p.LoggedIn {
				t.Fatal("anthropic should be logged in")
			}
			if p.Status != "connected" {
				t.Fatalf("anthropic status = %q, want connected", p.Status)
			}
			return
		}
	}
	t.Fatal("anthropic provider not found in response")
}

func TestHandleOAuthLogin_InvalidJSONReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/login", bytes.NewBufferString(`not-json`))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleOAuthLogin_UnsupportedProviderReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		strings.NewReader(`{"provider":"unknown-provider","method":"token","token":"abc"}`),
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleOAuthLogin_TokenMethodEmptyTokenReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		strings.NewReader(`{"provider":"anthropic","method":"token","token":""}`),
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleOAuthLogin_TokenMethodSuccess(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		strings.NewReader(`{"provider":"anthropic","method":"token","token":"my-api-key"}`),
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("status = %v, want ok", resp["status"])
	}
}

func TestHandleGetOAuthFlow_NotFoundReturns404(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/flows/nonexistent-id", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandlePollOAuthFlow_NotFoundReturns404(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/flows/nonexistent-id/poll", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandlePollOAuthFlow_NonDeviceCodeFlowReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	h.storeOAuthFlow(&oauthFlow{
		ID:       "browser-flow",
		Provider: oauthProviderAnthropic,
		Method:   oauthMethodBrowser,
		Status:   oauthFlowPending,
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/flows/browser-flow/poll", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleOAuthLogout_InvalidJSONReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/logout", bytes.NewBufferString(`not-json`))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleOAuthLogout_UnsupportedProviderReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/logout",
		bytes.NewBufferString(`{"provider":"unknown"}`),
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleOAuthCallback_MissingStateReturns400(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/oauth/callback", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "Missing state") {
		t.Fatalf("body = %q, want contains 'Missing state'", rec.Body.String())
	}
}

func TestHandleOAuthCallback_ErrorParamSetsFlowError(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	h.storeOAuthFlow(&oauthFlow{
		ID:         "flow-with-error",
		Provider:   oauthProviderAnthropic,
		Method:     oauthMethodBrowser,
		Status:     oauthFlowPending,
		OAuthState: "my-state-123",
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/oauth/callback?state=my-state-123&error=access_denied&error_description=User+denied",
		nil,
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "Authorization failed") {
		t.Fatalf("body = %q, want contains 'Authorization failed'", rec.Body.String())
	}

	flow, ok := h.getOAuthFlow("flow-with-error")
	if !ok {
		t.Fatal("flow not found after error callback")
	}
	if flow.Status != oauthFlowError {
		t.Fatalf("flow.Status = %q, want %q", flow.Status, oauthFlowError)
	}
}

func TestHandleOAuthCallback_MissingCodeSetsFlowError(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	h.storeOAuthFlow(&oauthFlow{
		ID:         "flow-no-code",
		Provider:   oauthProviderAnthropic,
		Method:     oauthMethodBrowser,
		Status:     oauthFlowPending,
		OAuthState: "state-no-code",
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?state=state-no-code", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestNormalizeOAuthProvider_AnthropicReturnsAnthropic(t *testing.T) {
	got, err := normalizeOAuthProvider("anthropic")
	if err != nil {
		t.Fatalf("normalizeOAuthProvider() error = %v", err)
	}
	if got != oauthProviderAnthropic {
		t.Fatalf("got = %q, want %q", got, oauthProviderAnthropic)
	}
}

func TestNormalizeOAuthProvider_InvalidReturnsError(t *testing.T) {
	_, err := normalizeOAuthProvider("bad-provider")
	if err == nil {
		t.Fatal("expected error for invalid provider")
	}
}

func TestIsOAuthMethodSupported(t *testing.T) {
	if !isOAuthMethodSupported(oauthProviderAnthropic, oauthMethodToken) {
		t.Fatal("token should be supported for anthropic")
	}
	if isOAuthMethodSupported(oauthProviderAnthropic, oauthMethodBrowser) {
		t.Fatal("browser should not be supported for anthropic")
	}
}
