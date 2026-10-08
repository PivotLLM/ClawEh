// ClawEh
// License: MIT

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/channels/device"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

// A WebUI save refuses a device listener on a network address with
// auto_approve on or no secret, naming the setting; loopback is allowed.
func TestValidateConfig_DeviceExposure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dev     config.DeviceChannelConfig
		wantKey string
	}{
		{"auto_approve on a network address", config.DeviceChannelConfig{Enabled: true, Host: "0.0.0.0", Token: "t", AutoApprove: true}, "channels.device.auto_approve"},
		{"no secret on a network address", config.DeviceChannelConfig{Enabled: true, Host: "0.0.0.0"}, "channels.device.token"},
		{"loopback with auto_approve and no secret", config.DeviceChannelConfig{Enabled: true, Host: "127.0.0.1", AutoApprove: true}, ""},
	} {
		cfg := validConfigForValidation()
		cfg.Channels.Device = tc.dev
		errs := validateConfig(cfg)
		if tc.wantKey == "" {
			if anyContains(errs, "channels.device") {
				t.Errorf("%s: refused: %v", tc.name, errs)
			}
			continue
		}
		if !anyContains(errs, tc.wantKey) {
			t.Errorf("%s: errs = %v, want one naming %s", tc.name, errs, tc.wantKey)
		}
	}
}

// GET /api/devices/pending carries the requesting client address.
func TestDevicePending_IncludesClientAddress(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	store, _, err := h.openDeviceStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePending(context.Background(), device.PendingPairing{
		DeviceID: "dev1", PublicKey: "pk", DisplayName: "Rabbit R1", Role: "node", RemoteIP: "203.0.113.7",
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.registerDeviceRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/devices/pending", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Pending []map[string]any `json:"pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Pending) != 1 || resp.Pending[0]["remote_ip"] != "203.0.113.7" {
		t.Fatalf("pending = %+v, want remote_ip 203.0.113.7", resp.Pending)
	}
}

// DELETE /api/devices/{id} closes the device's open connections through the
// running channel.
func TestDeviceRemove_Disconnects(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	var disconnected []string
	h.SetDeviceDisconnector(func(id string) { disconnected = append(disconnected, id) })
	mux := http.NewServeMux()
	h.registerDeviceRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/devices/dev1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(disconnected) != 1 || disconnected[0] != "dev1" {
		t.Fatalf("disconnected = %v, want [dev1]", disconnected)
	}
}

// Behind a trusted proxy the login lockout and the audit log use the address
// the proxy forwards; from anyone else the header is ignored.
func TestAuthLogin_TrustedProxy(t *testing.T) {
	initTestAudit(t)
	env := newAuthEnv(t, true)
	trusted, err := config.CompileTrustedProxies([]string{"192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	chain := middleware.TrustedProxy(func() *config.TrustedProxySet { return trusted }, env.mux)
	login := func(peer, realIP, user, pass string) int {
		t.Helper()
		body := fmt.Sprintf(`{"username":%q,"password":%q}`, user, pass)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
		req.RemoteAddr = peer + ":50000"
		req.Header.Set("X-Real-IP", realIP)
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)
		return rec.Code
	}

	// A's failures through the proxy lock A only. Different usernames, so only
	// the address threshold is reached.
	for i := range middleware.LoginIPFailures {
		login("192.0.2.1", "203.0.113.7", fmt.Sprintf("guess%d", i), "wrong password")
	}
	if code := login("192.0.2.1", "203.0.113.7", authTestUser, authTestPass); code != http.StatusTooManyRequests {
		t.Fatalf("forwarded address after lockout: status = %d, want 429", code)
	}
	// Another address behind the same proxy is not locked.
	if code := login("192.0.2.1", "203.0.113.8", authTestUser, authTestPass); code != http.StatusNoContent {
		t.Fatalf("other forwarded address: status = %d, want 204", code)
	}
	// From an untrusted peer the header is ignored: the peer is who it is.
	if code := login("198.51.100.9", "203.0.113.7", authTestUser, authTestPass); code != http.StatusNoContent {
		t.Fatalf("untrusted peer claiming a locked address: status = %d, want 204", code)
	}

	flushAudit(t)
	rows, err := audit.Default().Query(context.Background(), audit.Filter{Kind: audit.KindAuth, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	senders := map[string]bool{}
	for _, r := range rows {
		senders[r.Sender] = true
	}
	for _, want := range []string{"203.0.113.7", "203.0.113.8", "198.51.100.9"} {
		if !senders[want] {
			t.Errorf("no audit row from %s; senders = %v", want, senders)
		}
	}
	if senders["192.0.2.1"] {
		t.Errorf("audit recorded the proxy address; senders = %v", senders)
	}
}

// GET /api/devices marks a device whose assigned agent is no longer
// configured, so the Devices page can show the stale assignment.
func TestDeviceList_MarksMissingAgent(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	ctx := context.Background()
	store, _, err := h.openDeviceStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, dev := range []struct{ id, agent string }{{"dev1", "removed"}, {"dev2", "main"}, {"dev3", ""}} {
		reqID, err := store.CreatePending(ctx, device.PendingPairing{DeviceID: dev.id, PublicKey: "pk-" + dev.id, DisplayName: "Rabbit R1", Role: "node"})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Approve(ctx, reqID, []string{"node"}, nil); err != nil {
			t.Fatal(err)
		}
		if err := store.SetDeviceAgent(ctx, dev.id, dev.agent); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	h.registerDeviceRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/devices", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Devices []pairedDeviceView `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"dev1": true, "dev2": false, "dev3": false}
	if len(resp.Devices) != len(want) {
		t.Fatalf("devices = %+v, want %d", resp.Devices, len(want))
	}
	for _, d := range resp.Devices {
		if d.AgentMissing != want[d.DeviceID] {
			t.Errorf("%s: agent_missing = %v, want %v", d.DeviceID, d.AgentMissing, want[d.DeviceID])
		}
	}
}
