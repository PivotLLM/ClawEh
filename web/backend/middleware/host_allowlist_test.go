package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestHostCheck_AllowedAndRejected pins the Host rule: loopback names always,
// the caller's names (bind host, external_url host) in any case and with or
// without a port, and 421 for everything else — including the empty Host of a
// bare HTTP/1.0 request and a rebinding name that merely contains "localhost".
func TestHostCheck_AllowedAndRejected(t *testing.T) {
	names := []string{"192.168.1.10", "Claw.Example.COM:8443", ""}
	for _, tc := range []struct {
		name string
		host string
		want int
	}{
		{"localhost", "localhost", http.StatusOK},
		{"localhost with port", "localhost:18790", http.StatusOK},
		{"LOCALHOST upper", "LOCALHOST", http.StatusOK},
		{"127.0.0.1 with port", "127.0.0.1:18790", http.StatusOK},
		{"IPv6 loopback bracketed with port", "[::1]:18790", http.StatusOK},
		{"IPv6 loopback bracketed", "[::1]", http.StatusOK},
		{"IPv6 loopback bare", "::1", http.StatusOK},
		{"bind host", "192.168.1.10", http.StatusOK},
		{"bind host with port", "192.168.1.10:18790", http.StatusOK},
		{"external host", "claw.example.com", http.StatusOK},
		{"external host other port", "claw.example.com:443", http.StatusOK},
		{"other loopback address", "127.0.0.2", http.StatusMisdirectedRequest},
		{"rebinding name", "attacker.example", http.StatusMisdirectedRequest},
		{"localhost as a label", "localhost.attacker.example", http.StatusMisdirectedRequest},
		{"external as a label", "claw.example.com.attacker.example", http.StatusMisdirectedRequest},
		{"empty", "", http.StatusMisdirectedRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHostCheckHandler(names, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = tc.host
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("Host %q: status = %d, want %d", tc.host, rec.Code, tc.want)
			}
		})
	}
}

// TestHostCheck_NilIsLoopbackOnly covers the state before the first
// SetAllowedHosts: fail closed to loopback names, never open.
func TestHostCheck_NilIsLoopbackOnly(t *testing.T) {
	h := HostCheck(func() *HostAllowlist { return nil }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for host, want := range map[string]int{
		"localhost:18790":  http.StatusOK,
		"127.0.0.1":        http.StatusOK,
		"[::1]:18790":      http.StatusOK,
		"attacker.example": http.StatusMisdirectedRequest,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("Host %q: status = %d, want %d", host, rec.Code, want)
		}
	}
}

// TestHostCheck_RejectionBody: JSON under /api/ (what the WebUI client parses),
// plain text elsewhere, both 421.
func TestHostCheck_RejectionBody(t *testing.T) {
	h := newHostCheckHandler(nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range []struct {
		path     string
		wantCT   string
		wantBody string
	}{
		{"/api/config", "application/json", `{"error":"host not served by this gateway"}`},
		{"/", "text/plain", "Misdirected Request"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Host = "attacker.example"
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMisdirectedRequest {
			t.Fatalf("%s: status = %d, want %d", tc.path, rec.Code, http.StatusMisdirectedRequest)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.wantCT) {
			t.Fatalf("%s: Content-Type = %q, want prefix %q", tc.path, ct, tc.wantCT)
		}
		if !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Fatalf("%s: body = %q, want contains %q", tc.path, rec.Body.String(), tc.wantBody)
		}
	}
}

// TestHostCheck_HotSwap is the reload path: external_url changes, and the new
// name is served (and the old one refused) without recreating the listener.
func TestHostCheck_HotSwap(t *testing.T) {
	var current atomic.Pointer[HostAllowlist]
	current.Store(CompileHostAllowlist([]string{"old.example"}))
	h := HostCheck(current.Load, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	call := func(host string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := call("old.example"); got != http.StatusOK {
		t.Fatalf("before swap old.example: status = %d, want %d", got, http.StatusOK)
	}
	if got := call("new.example"); got != http.StatusMisdirectedRequest {
		t.Fatalf("before swap new.example: status = %d, want %d", got, http.StatusMisdirectedRequest)
	}

	current.Store(CompileHostAllowlist([]string{"new.example"}))
	if got := call("new.example"); got != http.StatusOK {
		t.Fatalf("after swap new.example: status = %d, want %d — a swapped list must apply to the next request", got, http.StatusOK)
	}
	if got := call("old.example"); got != http.StatusMisdirectedRequest {
		t.Fatalf("after swap old.example: status = %d, want %d", got, http.StatusMisdirectedRequest)
	}
	if got := call("localhost"); got != http.StatusOK {
		t.Fatalf("after swap localhost: status = %d, want %d — loopback is always served", got, http.StatusOK)
	}
}

func newHostCheckHandler(names []string, next http.Handler) http.Handler {
	list := CompileHostAllowlist(names)
	return HostCheck(func() *HostAllowlist { return list }, next)
}
