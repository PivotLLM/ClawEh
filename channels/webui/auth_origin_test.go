package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/PivotLLM/ClawEh/config"
)

// TestOriginAllowed_SameOrigin is the cross-site WebSocket hijack guard. Any
// page a user visits can open a WebSocket to their machine — CORS does not
// apply to WebSockets — so the origin check is what distinguishes the bundled
// UI from a hostile page. There is no allow-list to widen it any more.
func TestOriginAllowed_SameOrigin(t *testing.T) {
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
			if got := originAllowed(r); got != tc.want {
				t.Fatalf("originAllowed(host=%q origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
			}
		})
	}
}

func testChannel(t *testing.T, token string) *WebUIChannel {
	t.Helper()
	c, err := NewWebUIChannel(config.WebUIConfig{Token: token}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestAuthenticate_Subprotocol covers a non-browser client presenting the
// token as a subprotocol.
func TestAuthenticate_Subprotocol(t *testing.T) {
	const token = "webui-token-abcdefghijkl"
	c := testChannel(t, token)

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

// TestAuthenticate_QueryTokenNeverAccepted pins that a URL token is refused:
// a token in a URL is recorded by proxies, access logs and browser history,
// and the browser now authenticates with its login session instead.
func TestAuthenticate_QueryTokenNeverAccepted(t *testing.T) {
	const token = "webui-token-abcdefghijkl"
	r := httptest.NewRequest(http.MethodGet, "/webui/ws?token="+token, nil)
	if testChannel(t, token).authenticate(r) {
		t.Fatal("query token accepted")
	}
}

// TestAuthenticate_BearerStillWorks keeps the non-browser path intact.
func TestAuthenticate_BearerStillWorks(t *testing.T) {
	const token = "webui-token-abcdefghijkl"
	c := testChannel(t, token)

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

// TestAuthenticate_LoginSession is how the bundled UI connects: no token in
// the browser at all, just the session cookie that the injected validator
// recognises. Without a validator, or when it says no, the token rules apply.
func TestAuthenticate_LoginSession(t *testing.T) {
	const token = "webui-token-abcdefghijkl"
	c := testChannel(t, token)
	t.Cleanup(func() { SetSessionValidator(nil) })

	withCookie := httptest.NewRequest(http.MethodGet, "/webui/ws", nil)
	withCookie.AddCookie(&http.Cookie{Name: "claw_session", Value: "live"})
	bare := httptest.NewRequest(http.MethodGet, "/webui/ws", nil)

	if c.authenticate(withCookie) {
		t.Fatal("cookie accepted with no validator installed")
	}

	SetSessionValidator(func(r *http.Request) bool {
		ck, err := r.Cookie("claw_session")
		return err == nil && ck.Value == "live"
	})
	if !c.authenticate(withCookie) {
		t.Fatal("live session rejected")
	}
	if c.authenticate(bare) {
		t.Fatal("request without a session or token accepted")
	}
	// The token path is still there for non-browser clients.
	bare.Header.Set("Authorization", "Bearer "+token)
	if !c.authenticate(bare) {
		t.Fatal("Bearer token rejected while a validator is installed")
	}

	SetSessionValidator(nil)
	if c.authenticate(withCookie) {
		t.Fatal("cookie accepted after the validator was removed")
	}
}

// TestHandleWebSocket_RequiresSession drives the real upgrade path against a
// live server: without a session (or token) the handshake is refused with
// 401; with the session cookie it completes.
func TestHandleWebSocket_RequiresSession(t *testing.T) {
	c := testChannel(t, "webui-token-abcdefghijkl")
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
		SetSessionValidator(nil)
	})
	SetSessionValidator(func(r *http.Request) bool {
		ck, err := r.Cookie("claw_session")
		return err == nil && ck.Value == "live"
	})

	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/webui/ws"

	dial := func(cookie string) (*websocket.Conn, *http.Response, error) {
		h := http.Header{}
		if cookie != "" {
			h.Set("Cookie", "claw_session="+cookie)
		}
		return websocket.DefaultDialer.Dial(wsURL, h)
	}

	for _, cookie := range []string{"", "stale"} {
		conn, resp, err := dial(cookie)
		if resp != nil {
			if closeErr := resp.Body.Close(); closeErr != nil {
				t.Errorf("close dial response body: %v", closeErr)
			}
		}
		if err == nil {
			if closeErr := conn.Close(); closeErr != nil {
				t.Errorf("close websocket: %v", closeErr)
			}
			t.Fatalf("cookie %q: handshake succeeded without a live session", cookie)
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("cookie %q: response = %v, want 401", cookie, resp)
		}
	}

	conn, resp, err := dial("live")
	if resp != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("close dial response body: %v", closeErr)
		}
	}
	if err != nil {
		t.Fatalf("live session: handshake failed: %v", err)
	}
	if closeErr := conn.Close(); closeErr != nil {
		t.Errorf("close websocket: %v", closeErr)
	}
}
