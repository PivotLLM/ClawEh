package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// newPolicyHost builds an httpHost whose mux answers 200 on every route, with
// the given gateway config applied, for driving the handler chain directly.
func newPolicyHost(t *testing.T, gw config.GatewayConfig) *httpHost {
	t.Helper()
	h, err := newHTTPHost(hostOptions{})
	if err != nil {
		t.Fatalf("newHTTPHost() error = %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h.SetMux(mux)
	if err := h.ApplyPolicy(gw, nil); err != nil {
		t.Fatalf("ApplyPolicy() error = %v", err)
	}
	return h
}

// TestHTTPHostHostCheck pins which Host headers the shared listener answers to
// once the gateway config is applied: loopback names, the bind host and the
// host of gateway.external_url; anything else is 421 even from a loopback peer,
// which is the DNS-rebinding case.
func TestHTTPHostHostCheck(t *testing.T) {
	h := newPolicyHost(t, config.GatewayConfig{
		Host:        "192.168.1.10",
		Port:        18790,
		ExternalURL: "https://claw.example.com:8443",
	})
	cookie := loginCookie(t, h)
	for host, want := range map[string]int{
		"localhost:18790":        http.StatusOK,
		"127.0.0.1:18790":        http.StatusOK,
		"[::1]:18790":            http.StatusOK,
		"192.168.1.10:18790":     http.StatusOK,
		"claw.example.com:8443":  http.StatusOK,
		"claw.example.com":       http.StatusOK,
		"attacker.example:18790": http.StatusMisdirectedRequest,
		"192.168.1.11:18790":     http.StatusMisdirectedRequest,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Host = host
		req.AddCookie(cookie)
		h.handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q: status = %d, want %d", host, rec.Code, want)
		}
	}
}

// TestHTTPHostHostCheck_DerivedExternalURL: with no external_url the advertised
// URL is derived from the bind address, and its host is served. This is what
// keeps a loopback-only install reachable with no config at all.
func TestHTTPHostHostCheck_DerivedExternalURL(t *testing.T) {
	h := newPolicyHost(t, config.GatewayConfig{Host: "127.0.0.1", Port: 18790})
	for host, want := range map[string]int{
		"127.0.0.1:18790":  http.StatusOK,
		"localhost:18790":  http.StatusOK,
		"attacker.example": http.StatusMisdirectedRequest,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Host = host
		h.handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q: status = %d, want %d", host, rec.Code, want)
		}
	}
}

// TestHTTPHostApplyPolicy_HotSwap is the reload path: a changed external_url
// takes effect on the live listener for both the Host check and the trusted
// origin, and an invalid one leaves the running policy in place.
func TestHTTPHostApplyPolicy_HotSwap(t *testing.T) {
	h := newPolicyHost(t, config.GatewayConfig{Host: "127.0.0.1", Port: 18790, ExternalURL: "https://old.example"})
	cookie := loginCookie(t, h)
	get := func(host string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Host = host
		h.handler.ServeHTTP(rec, req)
		return rec.Code
	}
	post := func(origin string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Host = "127.0.0.1:18790"
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", origin)
		req.AddCookie(cookie)
		h.handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := get("old.example"); got != http.StatusOK {
		t.Fatalf("before: Host old.example status = %d, want %d", got, http.StatusOK)
	}
	if got := post("https://old.example"); got != http.StatusOK {
		t.Fatalf("before: Origin old.example status = %d, want %d", got, http.StatusOK)
	}

	if err := h.ApplyPolicy(config.GatewayConfig{Host: "127.0.0.1", Port: 18790, ExternalURL: "https://new.example"}, nil); err != nil {
		t.Fatalf("ApplyPolicy() error = %v", err)
	}
	if got := get("new.example"); got != http.StatusOK {
		t.Fatalf("after: Host new.example status = %d, want %d", got, http.StatusOK)
	}
	if got := get("old.example"); got != http.StatusMisdirectedRequest {
		t.Fatalf("after: Host old.example status = %d, want %d", got, http.StatusMisdirectedRequest)
	}
	if got := post("https://new.example"); got != http.StatusOK {
		t.Fatalf("after: Origin new.example status = %d, want %d", got, http.StatusOK)
	}
	if got := post("https://old.example"); got != http.StatusForbidden {
		t.Fatalf("after: Origin old.example status = %d, want %d", got, http.StatusForbidden)
	}

	if err := h.ApplyPolicy(config.GatewayConfig{Host: "127.0.0.1", Port: 18790, ExternalURL: "not a url"}, nil); err == nil {
		t.Fatal("ApplyPolicy() accepted an invalid external_url")
	}
	if got := get("new.example"); got != http.StatusOK {
		t.Fatalf("after rejected ApplyPolicy: status = %d, want %d — a bad external_url must leave the running policy alone", got, http.StatusOK)
	}
}

// TestHTTPHostCrossOrigin drives the CSRF policy through the real chain: a
// cross-site POST to the API is refused, same-origin and non-browser POSTs
// pass, and the token-gated message route is exempt.
func TestHTTPHostCrossOrigin(t *testing.T) {
	h := newPolicyHost(t, config.GatewayConfig{Host: "127.0.0.1", Port: 18790})
	cookie := loginCookie(t, h)
	for _, tc := range []struct {
		name      string
		path      string
		fetchSite string
		want      int
	}{
		{"cross-site API POST", "/api/config", "cross-site", http.StatusForbidden},
		{"same-origin API POST", "/api/config", "same-origin", http.StatusOK},
		{"non-browser API POST", "/api/config", "", http.StatusOK},
		{"cross-site message route", "/api/message/tok123", "cross-site", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			req.RemoteAddr = "127.0.0.1:5000"
			req.Host = "127.0.0.1:18790"
			if tc.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			if tc.path != "/api/message/tok123" { // the message route is token-gated, not session-gated
				req.AddCookie(cookie)
			}
			h.handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// TestHTTPHostSecurityHeaders: the hardening headers reach the wire on every
// response, and no-store only on /api/*.
func TestHTTPHostSecurityHeaders(t *testing.T) {
	h := newPolicyHost(t, config.GatewayConfig{Host: "127.0.0.1", Port: 18790})
	cookie := loginCookie(t, h)
	for path, wantCache := range map[string]string{"/api/config": "no-store", "/": ""} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Host = "127.0.0.1:18790"
		req.AddCookie(cookie)
		h.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want %d", path, rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
			t.Errorf("%s: Content-Security-Policy = %q", path, got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q", path, got)
		}
		if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s: Referrer-Policy = %q", path, got)
		}
		if got := rec.Header().Get("Cache-Control"); got != wantCache {
			t.Errorf("%s: Cache-Control = %q, want %q", path, got, wantCache)
		}
	}
}

// TestHTTPHostChainOrder: the IP allowlist runs before the Host check, so an
// off-list peer sees 403 and never learns whether its Host would have been
// served; a bad Host from an allowed peer sees 421.
func TestHTTPHostChainOrder(t *testing.T) {
	h := newPolicyHost(t, config.GatewayConfig{Host: "127.0.0.1", Port: 18790})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:5000"
	req.Host = "attacker.example"
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("off-list peer with bad Host: status = %d, want %d (IP allowlist first)", rec.Code, http.StatusForbidden)
	}
}
