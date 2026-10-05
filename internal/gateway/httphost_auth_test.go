package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

const (
	testAdminUser = "alice"
	testAdminPass = "a perfectly fine password"
)

// loginCookie installs an auth store with one admin account on h and returns
// the session cookie of a logged-in browser, the way the WebUI holds one after
// POST /api/auth/login over plain HTTP.
func loginCookie(t *testing.T, h *httpHost) *http.Cookie {
	t.Helper()
	path := admin.Path(t.TempDir())
	if err := admin.Write(path, testAdminUser, testAdminPass); err != nil {
		t.Fatalf("admin.Write: %v", err)
	}
	store := middleware.NewAuthStore(path)
	h.SetAuth(store)
	id, ok := store.Login(testAdminUser, testAdminPass)
	if !ok {
		t.Fatal("Login failed with the written credentials")
	}
	return &http.Cookie{Name: middleware.SessionCookieInsecure, Value: id}
}

// authGet drives a loopback GET through the whole chain.
func authGet(h *httpHost, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "127.0.0.1:18790"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	h.handler.ServeHTTP(rec, req)
	return rec
}

// TestHTTPHostAuth_APINeedsSession: on the wired host an unauthenticated GET
// /api/config is 401 (with the security headers), the health probe is served,
// and the same request with a session cookie reaches the handler.
func TestHTTPHostAuth_APINeedsSession(t *testing.T) {
	h := newPolicyHost(t, config.GatewayConfig{Host: "127.0.0.1", Port: 18790})
	cookie := loginCookie(t, h)

	rec := authGet(h, "/api/config", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/config: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(rec.Body.String(), "authentication required") {
		t.Fatalf("401 body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("401 lacks the security headers: X-Content-Type-Options = %q", got)
	}

	if rec := authGet(h, "/health", nil); rec.Code != http.StatusOK {
		t.Fatalf("unauthenticated GET /health: status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec := authGet(h, "/", nil); rec.Code != http.StatusOK {
		t.Fatalf("unauthenticated GET / (SPA shell): status = %d, want %d", rec.Code, http.StatusOK)
	}

	if rec := authGet(h, "/api/config", cookie); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config with a session: status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestHTTPHostAuth_NoStoreFailsClosed: before SetAuth every protected request
// is refused with the `claw admin` hint.
func TestHTTPHostAuth_NoStoreFailsClosed(t *testing.T) {
	h := newPolicyHost(t, config.GatewayConfig{Host: "127.0.0.1", Port: 18790})
	rec := authGet(h, "/api/config", nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "claw admin") {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
}
