package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestCrossOrigin_Policy pins the CSRF rule on the shared listener: an unsafe
// request a browser marks as cross-site is refused; same-origin, user-initiated
// (none) and non-browser requests pass; safe methods always pass; a route with
// its own authentication (bypass pattern) and a trusted origin are let through.
func TestCrossOrigin_Policy(t *testing.T) {
	policy, err := NewCrossOriginProtection(
		[]string{"https://claw.example.com"},
		[]string{"POST /api/message/{token}", "/webhook/line"},
	)
	if err != nil {
		t.Fatalf("NewCrossOriginProtection() error = %v", err)
	}
	h := CrossOrigin(func() *http.CrossOriginProtection { return policy }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, tc := range []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		want    int
	}{
		{"cross-site POST", http.MethodPost, "/api/config", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"cross-site PUT", http.MethodPut, "/api/config", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"cross-site DELETE", http.MethodDelete, "/api/sessions/x", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"same-site POST", http.MethodPost, "/api/config", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"same-origin POST", http.MethodPost, "/api/config", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		{"none (typed URL) POST", http.MethodPost, "/api/config", map[string]string{"Sec-Fetch-Site": "none"}, http.StatusOK},
		{"non-browser POST", http.MethodPost, "/api/config", nil, http.StatusOK},
		{"cross-site GET", http.MethodGet, "/api/config", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusOK},
		{"cross-site HEAD", http.MethodHead, "/", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusOK},
		{"cross-site OPTIONS", http.MethodOptions, "/api/config", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusOK},
		// Old browser without Sec-Fetch-Site: Origin is compared with Host.
		{"foreign Origin, no Sec-Fetch-Site", http.MethodPost, "/api/config", map[string]string{"Origin": "http://attacker.example"}, http.StatusForbidden},
		{"matching Origin, no Sec-Fetch-Site", http.MethodPost, "/api/config", map[string]string{"Origin": "http://example.com"}, http.StatusOK},
		{"trusted Origin cross-site", http.MethodPost, "/api/config", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://claw.example.com"}, http.StatusOK},
		{"trusted host, other scheme", http.MethodPost, "/api/config", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://claw.example.com"}, http.StatusForbidden},
		{"exempt message route", http.MethodPost, "/api/message/abc123", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusOK},
		{"exempt webhook", http.MethodPost, "/webhook/line", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusOK},
		{"exempt pattern does not widen", http.MethodPost, "/api/message", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"SPA path cross-site POST", http.MethodPost, "/", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s %s %v: status = %d, want %d", tc.method, tc.path, tc.headers, rec.Code, tc.want)
			}
		})
	}
}

// TestCrossOrigin_NilIsStrict covers the state before the first SetCrossOrigin:
// no trusted origins, no bypasses — closed, not open or panicking.
func TestCrossOrigin_NilIsStrict(t *testing.T) {
	h := CrossOrigin(func() *http.CrossOriginProtection { return nil }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/message/abc", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("nil policy: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// TestCrossOrigin_RejectionBody: JSON under /api/, plain text elsewhere.
func TestCrossOrigin_RejectionBody(t *testing.T) {
	h := CrossOrigin(func() *http.CrossOriginProtection { return nil }, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range []struct {
		path     string
		wantCT   string
		wantBody string
	}{
		{"/api/config", "application/json", `{"error":"cross-origin request rejected"}`},
		{"/", "text/plain", "Forbidden"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, tc.path, nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want %d", tc.path, rec.Code, http.StatusForbidden)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.wantCT) {
			t.Fatalf("%s: Content-Type = %q, want prefix %q", tc.path, ct, tc.wantCT)
		}
		if !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Fatalf("%s: body = %q, want contains %q", tc.path, rec.Body.String(), tc.wantBody)
		}
	}
}

// TestCrossOrigin_HotSwap is the reload path: a changed external_url becomes
// the trusted origin on the live listener, and the old one stops being trusted.
func TestCrossOrigin_HotSwap(t *testing.T) {
	var current atomic.Pointer[http.CrossOriginProtection]
	store := func(origins ...string) {
		p, err := NewCrossOriginProtection(origins, nil)
		if err != nil {
			t.Fatalf("NewCrossOriginProtection(%v) error = %v", origins, err)
		}
		current.Store(p)
	}
	store("https://old.example")
	h := CrossOrigin(current.Load, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	call := func(origin string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", origin)
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := call("https://old.example"); got != http.StatusOK {
		t.Fatalf("before swap old: status = %d, want %d", got, http.StatusOK)
	}
	store("https://new.example")
	if got := call("https://new.example"); got != http.StatusOK {
		t.Fatalf("after swap new: status = %d, want %d", got, http.StatusOK)
	}
	if got := call("https://old.example"); got != http.StatusForbidden {
		t.Fatalf("after swap old: status = %d, want %d — a replaced policy must drop the old origin", got, http.StatusForbidden)
	}
}

func TestNewCrossOriginProtection_InvalidOrigin(t *testing.T) {
	for _, origin := range []string{"claw.example.com", "https://claw.example.com/path", ""} {
		if _, err := NewCrossOriginProtection([]string{origin}, nil); err == nil {
			t.Errorf("NewCrossOriginProtection(%q) accepted an invalid origin", origin)
		}
	}
}
