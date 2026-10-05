package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAuth_WebUIChannelTokenPassesToChannel pins the non-browser WebUI-channel
// path: a request under /webui/ that presents the channel token (Bearer header
// or the claw-token subprotocol) reaches the channel, which validates the token
// itself; the same request without a token is refused by the login.
func TestAuth_WebUIChannelTokenPassesToChannel(t *testing.T) {
	store := NewAuthStore(t.TempDir() + "/credentials.json")
	reached := false
	h := Auth(func() *AuthStore { return store }, func() *AuthExempt { return nil },
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))

	for _, tc := range []struct {
		name     string
		path     string
		header   map[string]string
		wantPass bool
	}{
		{"bearer token", "/webui/ws", map[string]string{"Authorization": "Bearer abc"}, true},
		{"claw-token subprotocol", "/webui/ws", map[string]string{"Sec-WebSocket-Protocol": "claw-token, claw-token.abc"}, true},
		{"no token", "/webui/ws", nil, false},
		{"other subprotocol", "/webui/ws", map[string]string{"Sec-WebSocket-Protocol": "graphql-ws"}, false},
		{"bearer outside webui", "/api/config", map[string]string{"Authorization": "Bearer abc"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if reached != tc.wantPass {
				t.Fatalf("reached handler = %v, want %v (status %d)", reached, tc.wantPass, rec.Code)
			}
			if !tc.wantPass && rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
		})
	}
}
