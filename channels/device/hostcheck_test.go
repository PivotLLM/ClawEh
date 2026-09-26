package device

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHostAllowlist: the listener answers to its own names and to IP literals,
// and to nothing else — that is the DNS-rebinding guard.
func TestHostAllowlist(t *testing.T) {
	al := newHostAllowlist("0.0.0.0",
		[]string{"https://claw.example.com", "http://gw.example.org:8080", ""},
		[]string{"Claw.Local"})

	for _, tc := range []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"localhost:18791", true},
		{"127.0.0.1:18791", true},
		{"[::1]:18791", true},
		{"[::1]", true},
		{"192.168.1.20:18791", true}, // LAN IP from the QR when external_url is unset
		{"claw.example.com", true},
		{"CLAW.EXAMPLE.COM:443", true},
		{"gw.example.org", true},
		{"claw.local:18791", true},
		{"", false},
		{"evil.example.net", false},
		{"claw.example.com.evil.net", false},
		{"sub.claw.example.com", false},
	} {
		if got := al.allows(tc.host); got != tc.want {
			t.Errorf("allows(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// TestHostCheckHandler: a request for an unknown Host is answered 421 and never
// reaches the upgrade handler; a known one passes through.
func TestHostCheckHandler(t *testing.T) {
	al := newHostAllowlist("127.0.0.1", []string{"https://claw.example.com"}, nil)
	reached := false
	h := hostCheckHandler(al, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	for _, tc := range []struct {
		host string
		code int
	}{
		{"rebound.attacker.net:18791", http.StatusMisdirectedRequest},
		{"claw.example.com", http.StatusOK},
		{"127.0.0.1:18791", http.StatusOK},
	} {
		reached = false
		req := httptest.NewRequest(http.MethodGet, "http://placeholder/", nil)
		req.Host = tc.host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.code || reached != (tc.code == http.StatusOK) {
			t.Errorf("Host %q: code=%d reached=%v, want %d", tc.host, rec.Code, reached, tc.code)
		}
	}
}
