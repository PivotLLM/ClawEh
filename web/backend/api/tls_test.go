package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
)

// tlsTestHandler is a handler on a fresh test config with its routes mounted.
func tlsTestHandler(t *testing.T) (*Handler, *http.ServeMux, string) {
	t.Helper()
	configPath := setupTestEnv(t)
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux, configPath
}

func serveJSON(t *testing.T, mux *http.ServeMux, method, path, body string, out any) int {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return rec.Code
}

// selfSignedPair generates a self-signed pair under a temp dir as of now and
// returns its paths.
func selfSignedPair(t *testing.T, now time.Time) (certPath, keyPath string) {
	t.Helper()
	m, err := tlscert.Load(tlscert.Options{DataDir: t.TempDir(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	info := m.Info()
	return info.CertFile, info.KeyFile
}

// TestGetTLS_Shape pins the GET /api/tls document the WebUI's TLS page reads.
func TestGetTLS_Shape(t *testing.T) {
	_, mux, _ := tlsTestHandler(t)
	var raw map[string]json.RawMessage
	if code := serveJSON(t, mux, http.MethodGet, "/api/tls", "", &raw); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for _, key := range []string{
		"mode", "source", "cert_file", "key_file", "extra_names", "tls_port",
		"http_host", "http_port", "external_url", "urls", "certificate", "restart_required",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q", key)
		}
	}
	var got tlsStatusResponse
	if code := serveJSON(t, mux, http.MethodGet, "/api/tls", "", &got); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got.Mode != "all" || got.Source != "self-signed" || got.TLSPort != 18443 || got.HTTPPort != 18790 || got.HTTPHost != "127.0.0.1" {
		t.Errorf("defaults = %+v", got)
	}
	if got.ExtraNames == nil || got.URLs.HTTP == nil || got.URLs.HTTPS == nil {
		t.Errorf("lists must be [] not null: %+v", got)
	}
	if got.URLs.Localhost != "http://127.0.0.1:18790/" {
		t.Errorf("urls.localhost = %q", got.URLs.Localhost)
	}
	for _, u := range got.URLs.HTTPS {
		if !strings.HasPrefix(u, "https://") || !strings.HasSuffix(u, ":18443/") {
			t.Errorf("urls.https entry %q", u)
		}
	}
	// No self-signed pair yet (the gateway generates it on the first HTTPS
	// start): an absent certificate, not an error.
	if got.Certificate == nil || got.Certificate.Present || got.Certificate.Error != "" {
		t.Errorf("certificate = %+v, want present=false", got.Certificate)
	}
	if got.RestartRequired {
		t.Error("restart_required without a running gateway")
	}
}

// TestGetTLS_RestartRequired: a saved listener setting that differs from what
// the running gateway bound is reported; an unrelated change is not.
func TestGetTLS_RestartRequired(t *testing.T) {
	h, mux, _ := tlsTestHandler(t)
	cfg, err := h.currentConfig()
	if err != nil {
		t.Fatal(err)
	}
	h.SetBootListeners(cfg.Gateway.Listeners())
	get := func() tlsStatusResponse {
		var got tlsStatusResponse
		if code := serveJSON(t, mux, http.MethodGet, "/api/tls", "", &got); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		return got
	}
	if get().RestartRequired {
		t.Fatal("restart_required right after boot")
	}
	if code := serveJSON(t, mux, http.MethodPatch, "/api/config", `{"gateway":{"tls":{"extra_names":["claw.lan"]}}}`, nil); code != http.StatusOK {
		t.Fatalf("patch extra_names: %d", code)
	}
	if got := get(); got.RestartRequired || !slices.Equal(got.ExtraNames, []string{"claw.lan"}) {
		t.Errorf("extra_names change: %+v (applied by regenerate, not a restart)", got)
	}
	if code := serveJSON(t, mux, http.MethodPatch, "/api/config", `{"gateway":{"tls_port":19443}}`, nil); code != http.StatusOK {
		t.Fatalf("patch tls_port: %d", code)
	}
	if got := get(); !got.RestartRequired || got.TLSPort != 19443 {
		t.Errorf("tls_port change: restart_required=%v tls_port=%d", got.RestartRequired, got.TLSPort)
	}
}

// TestPatchConfig_TLSListenerValidation: the listener checks run on a WebUI
// save — an unknown mode, clashing ports, and certificate paths the gateway
// could not start on are refused; turning HTTPS off is accepted.
func TestPatchConfig_TLSListenerValidation(t *testing.T) {
	_, mux, configPath := tlsTestHandler(t)
	certPath, keyPath := selfSignedPair(t, time.Now())
	for _, c := range []struct {
		name, body, want string
	}{
		{"unknown mode", `{"gateway":{"tls":{"mode":"lan"}}}`, "gateway.tls.mode"},
		{"ports clash", `{"gateway":{"tls_port":18790}}`, "must differ"},
		{"half pair", `{"gateway":{"tls":{"cert_file":"` + certPath + `"}}}`, "set together"},
		{"unreadable key", `{"gateway":{"tls":{"cert_file":"` + certPath + `","key_file":"/nonexistent/claw.key"}}}`, "key_file /nonexistent/claw.key cannot be read"},
	} {
		var resp struct {
			Status string   `json:"status"`
			Errors []string `json:"errors"`
		}
		if code := serveJSON(t, mux, http.MethodPatch, "/api/config", c.body, &resp); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", c.name, code)
			continue
		}
		if !strings.Contains(strings.Join(resp.Errors, "; "), c.want) {
			t.Errorf("%s: errors %v, want %q", c.name, resp.Errors, c.want)
		}
	}
	if code := serveJSON(t, mux, http.MethodPatch, "/api/config",
		`{"gateway":{"host":"0.0.0.0","tls":{"mode":"off"}}}`, nil); code != http.StatusOK {
		t.Fatalf("network HTTP with HTTPS off must be accepted: %d", code)
	}
	if code := serveJSON(t, mux, http.MethodPatch, "/api/config",
		`{"gateway":{"tls":{"mode":"all","cert_file":"`+certPath+`","key_file":"`+keyPath+`"}}}`, nil); code != http.StatusOK {
		t.Fatalf("a valid pair must be accepted: %d", code)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway.TLS.CertFile != certPath || cfg.Gateway.Host != "0.0.0.0" || cfg.Gateway.TLS.EffectiveMode() != "all" {
		t.Errorf("saved gateway = %+v", cfg.Gateway)
	}
}

// TestValidateTLS: a good pair is described; a missing key file, a
// mismatched pair and an expired certificate are 400 naming the problem.
func TestValidateTLS(t *testing.T) {
	_, mux, _ := tlsTestHandler(t)
	now := time.Now()
	certPath, keyPath := selfSignedPair(t, now)
	_, otherKey := selfSignedPair(t, now)
	// Generated eleven years ago: a self-signed certificate lasts ten.
	oldCert, oldKey := selfSignedPair(t, now.AddDate(-11, 0, 0))
	body := func(cert, key string) string {
		b, err := json.Marshal(map[string]string{"cert_file": cert, "key_file": key})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	var c tlsCertificateJSON
	if code := serveJSON(t, mux, http.MethodPost, "/api/tls/validate", body(certPath, keyPath), &c); code != http.StatusOK {
		t.Fatalf("good pair: status %d", code)
	}
	if !c.Present || c.Fingerprint == "" || !slices.Contains(c.Names, "localhost") || !c.SelfSigned {
		t.Errorf("certificate = %+v", c)
	}
	if _, err := time.Parse(time.RFC3339, c.NotAfter); err != nil {
		t.Errorf("not_after %q is not RFC3339", c.NotAfter)
	}

	missingKey := filepath.Join(t.TempDir(), "missing.key")
	for _, tc := range []struct {
		name, body, want string
	}{
		{"missing key file", body(certPath, missingKey), "key_file " + missingKey},
		{"mismatched pair", body(certPath, otherKey), "do not form a pair"},
		{"expired", body(oldCert, oldKey), "expired"},
		{"bad JSON", "{", "invalid JSON"},
	} {
		var e struct {
			Error string `json:"error"`
		}
		if code := serveJSON(t, mux, http.MethodPost, "/api/tls/validate", tc.body, &e); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", tc.name, code)
		}
		if !strings.Contains(e.Error, tc.want) {
			t.Errorf("%s: error %q, want it to contain %q", tc.name, e.Error, tc.want)
		}
	}
}

// TestRegenerateTLS: the running self-signed certificate is replaced (new
// fingerprint, saved extra names included); an operator certificate is a
// 409; no HTTPS listener is a 503.
func TestRegenerateTLS(t *testing.T) {
	h, mux, _ := tlsTestHandler(t)
	var e struct {
		Error string `json:"error"`
	}
	if code := serveJSON(t, mux, http.MethodPost, "/api/tls/regenerate", "", &e); code != http.StatusServiceUnavailable {
		t.Errorf("no manager: status %d, want 503", code)
	}

	m, err := tlscert.Load(tlscert.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h.SetTLSManager(m)
	before := m.Info().Fingerprint
	if code := serveJSON(t, mux, http.MethodPatch, "/api/config", `{"gateway":{"tls":{"extra_names":["claw.lan"]}}}`, nil); code != http.StatusOK {
		t.Fatalf("patch: %d", code)
	}
	var got tlsCertificateJSON
	if code := serveJSON(t, mux, http.MethodPost, "/api/tls/regenerate", "", &got); code != http.StatusOK {
		t.Fatalf("regenerate: status %d", code)
	}
	if !got.Present || got.Fingerprint == before || got.Fingerprint != m.Info().Fingerprint {
		t.Errorf("fingerprint %+v, was %s", got, before)
	}
	if !slices.Contains(got.Names, "claw.lan") {
		t.Errorf("names %v lack the saved extra name", got.Names)
	}

	certPath, keyPath := selfSignedPair(t, time.Now())
	fileMgr, err := tlscert.Load(tlscert.Options{CertFile: certPath, KeyFile: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	h.SetTLSManager(fileMgr)
	if code := serveJSON(t, mux, http.MethodPost, "/api/tls/regenerate", "", &e); code != http.StatusConflict {
		t.Errorf("file source: status %d, want 409", code)
	}
}
