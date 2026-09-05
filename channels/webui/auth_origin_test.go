package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// TestOriginAllowed_EmptyMeansSameOrigin is the cross-site WebSocket hijack
// guard. Any page a user visits can open a WebSocket to their machine — CORS
// does not apply to WebSockets — so the origin check is what distinguishes the
// bundled UI from a hostile page. Setup used to write ["*"] here, which turned
// it off on every install.
func TestOriginAllowed_EmptyMeansSameOrigin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"same origin, localhost", "localhost:18790", "http://localhost:18790", true},
		{"same origin, LAN address", "192.168.1.5:18790", "http://192.168.1.5:18790", true},
		{"same origin, proxied hostname", "claw.example.com", "https://claw.example.com", true},
		{"same host, case differs", "Claw.Example.com", "https://claw.example.com", true},
		{"no Origin header is not a browser", "localhost:18790", "", true},

		{"hostile page", "localhost:18790", "https://evil.example.com", false},
		{"different port on the same host", "localhost:18790", "http://localhost:5173", false},
		{"subdomain is not the same host", "claw.example.com", "https://evil.claw.example.com", false},
		{"unparsable origin", "localhost:18790", "://not a url", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/webui/ws", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := originAllowed(r, nil); got != tc.want {
				t.Fatalf("originAllowed(host=%q origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
			}
		})
	}
}

// TestOriginAllowed_ExplicitListIsHonoured covers the escape hatch a frontend
// dev server needs, and confirms an explicit list still excludes everything
// else.
func TestOriginAllowed_ExplicitListIsHonoured(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed []string
		origin  string
		want    bool
	}{
		{"listed origin", []string{"http://localhost:5173"}, "http://localhost:5173", true},
		{"unlisted origin", []string{"http://localhost:5173"}, "https://evil.example.com", false},
		{"wildcard allows any", []string{"*"}, "https://evil.example.com", true},
		{"list excludes same-origin unless listed", []string{"http://localhost:5173"}, "http://localhost:18790", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/webui/ws", nil)
			r.Host = "localhost:18790"
			r.Header.Set("Origin", tc.origin)
			if got := originAllowed(r, tc.allowed); got != tc.want {
				t.Fatalf("originAllowed(%v, %q) = %v, want %v", tc.allowed, tc.origin, got, tc.want)
			}
		})
	}
}

func testChannel(t *testing.T, token string, allowQuery bool) *WebUIChannel {
	t.Helper()
	c, err := NewWebUIChannel(config.WebUIConfig{Token: token, AllowTokenQuery: allowQuery}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestAuthenticate_Subprotocol covers how the browser now presents the token.
// A WebSocket handshake from a browser cannot carry an Authorization header, so
// without this the only options are a query-string token or no auth at all.
func TestAuthenticate_Subprotocol(t *testing.T) {
	const token = "webui-token-abcdefghijkl"
	c := testChannel(t, token, false)

	for _, tc := range []struct {
		name     string
		protocol string
		want     bool
	}{
		{"marker then token", TokenSubprotocol + ", " + token, true},
		{"token alone", token, true},
		{"marker alone is not a token", TokenSubprotocol, false},
		{"wrong token", TokenSubprotocol + ", not-the-token", false},
		{"no subprotocol", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/webui/ws", nil)
			if tc.protocol != "" {
				r.Header.Set("Sec-WebSocket-Protocol", tc.protocol)
			}
			if got := c.authenticate(r); got != tc.want {
				t.Fatalf("authenticate(protocol=%q) = %v, want %v", tc.protocol, got, tc.want)
			}
		})
	}
}

// TestAuthenticate_QueryTokenIsOptIn pins that a URL token only works when the
// operator has asked for it. It is off by default because a token in a URL is
// recorded by proxies, access logs and browser history.
func TestAuthenticate_QueryTokenIsOptIn(t *testing.T) {
	const token = "webui-token-abcdefghijkl"

	r := httptest.NewRequest(http.MethodGet, "/webui/ws?token="+token, nil)
	if testChannel(t, token, false).authenticate(r) {
		t.Fatal("query token accepted with AllowTokenQuery=false")
	}
	if !testChannel(t, token, true).authenticate(r) {
		t.Fatal("query token rejected with AllowTokenQuery=true")
	}
}

// TestAuthenticate_BearerStillWorks keeps the non-browser path intact.
func TestAuthenticate_BearerStillWorks(t *testing.T) {
	const token = "webui-token-abcdefghijkl"
	c := testChannel(t, token, false)

	r := httptest.NewRequest(http.MethodGet, "/webui/ws", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if !c.authenticate(r) {
		t.Fatal("Bearer token rejected")
	}

	r = httptest.NewRequest(http.MethodGet, "/webui/ws", nil)
	r.Header.Set("Authorization", "Bearer wrong")
	if c.authenticate(r) {
		t.Fatal("wrong Bearer token accepted")
	}
}
