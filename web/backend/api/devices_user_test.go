// ClawEh
// License: MIT

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/channels/device"
	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

// pairTestDevice pairs a device with id deviceID in the handler's store.
func pairTestDevice(t *testing.T, h *Handler, deviceID string) {
	t.Helper()
	ctx := context.Background()
	store, _, err := h.openDeviceStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := store.CreatePending(ctx, device.PendingPairing{DeviceID: deviceID, PublicKey: "pk-" + deviceID, DisplayName: "Rabbit R1", Role: "node"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Approve(ctx, reqID, []string{"node"}, nil); err != nil {
		t.Fatal(err)
	}
}

// listDevices returns GET /api/devices' devices.
func listDevices(t *testing.T, mux *http.ServeMux) []pairedDeviceView {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/devices", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/devices: status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Devices []pairedDeviceView `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Devices
}

// POST /api/devices/{id}/user stores the device's user, GET /api/devices
// returns it with the admin account's username as the default, and a refused
// name is a 400 that leaves the stored value alone.
func TestDeviceUser_RoundTrip(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	credsPath := admin.Path(t.TempDir())
	if err := admin.Write(credsPath, "bob", "a long enough password"); err != nil {
		t.Fatal(err)
	}
	h.SetAuth(middleware.NewAuthStore(credsPath))
	pairTestDevice(t, h, "dev1")
	mux := http.NewServeMux()
	h.registerDeviceRoutes(mux)

	post := func(id, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices/"+id+"/user", strings.NewReader(body)))
		return rec
	}

	devs := listDevices(t, mux)
	if len(devs) != 1 || devs[0].UserName != "" || devs[0].UserDefault != "bob" {
		t.Fatalf("before: devices = %+v, want user_name \"\" and user_default bob", devs)
	}

	if rec := post("dev1", `{"user_name":"  Alice  "}`); rec.Code != http.StatusOK {
		t.Fatalf("set: status = %d: %s", rec.Code, rec.Body.String())
	}
	devs = listDevices(t, mux)
	if len(devs) != 1 || devs[0].UserName != "Alice" || devs[0].UserDefault != "bob" {
		t.Fatalf("after set: devices = %+v, want user_name Alice and user_default bob", devs)
	}

	for _, body := range []string{`{"user_name":"` + strings.Repeat("a", device.MaxDeviceUserNameLen+1) + `"}`, `{"user_name":"Al\u0007ice"}`} {
		rec := post("dev1", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("refused name: status = %d, want 400: %s", rec.Code, rec.Body.String())
		}
		var resp map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp["error"] == "" {
			t.Fatalf("refused name: body %s, want an error message", rec.Body.String())
		}
	}
	if devs = listDevices(t, mux); devs[0].UserName != "Alice" {
		t.Fatalf("a refused name changed the stored one: %q", devs[0].UserName)
	}

	if rec := post("missing", `{"user_name":"Alice"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown device: status = %d, want 404", rec.Code)
	}

	if rec := post("dev1", `{"user_name":""}`); rec.Code != http.StatusOK {
		t.Fatalf("clear: status = %d: %s", rec.Code, rec.Body.String())
	}
	if devs = listDevices(t, mux); devs[0].UserName != "" {
		t.Fatalf("after clear: user_name = %q, want empty", devs[0].UserName)
	}
}

// Without an admin account, user_default is empty.
func TestDeviceList_NoAdminAccountNoDefaultUser(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	pairTestDevice(t, h, "dev1")
	mux := http.NewServeMux()
	h.registerDeviceRoutes(mux)
	devs := listDevices(t, mux)
	if len(devs) != 1 || devs[0].UserDefault != "" {
		t.Fatalf("devices = %+v, want user_default empty", devs)
	}
}

// operator.name, set through PATCH /api/config, is the default user GET
// /api/devices returns in place of the admin account's username; GET
// /api/config returns it as written, and a refused one is a 400.
func TestDeviceList_OperatorNameIsDefaultUser(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	credsPath := admin.Path(t.TempDir())
	if err := admin.Write(credsPath, "bob", "a long enough password"); err != nil {
		t.Fatal(err)
	}
	h.SetAuth(middleware.NewAuthStore(credsPath))
	pairTestDevice(t, h, "dev1")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := patch(`{"operator":{"name":"Alice"}}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH operator.name: status = %d: %s", rec.Code, rec.Body.String())
	}
	if devs := listDevices(t, mux); len(devs) != 1 || devs[0].UserDefault != "Alice" {
		t.Fatalf("devices = %+v, want user_default Alice", devs)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var got struct {
		Operator struct {
			Name string `json:"name"`
		} `json:"operator"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Operator.Name != "Alice" {
		t.Fatalf("GET /api/config operator.name = %q (err %v), want Alice: %s", got.Operator.Name, err, rec.Body.String())
	}

	if rec := patch(`{"operator":{"name":"Al\u0007ice"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH a control character: status = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	if rec := patch(`{"operator":{"name":""}}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH clear: status = %d: %s", rec.Code, rec.Body.String())
	}
	if devs := listDevices(t, mux); devs[0].UserDefault != "bob" {
		t.Fatalf("after clear: user_default = %q, want the admin account's bob", devs[0].UserDefault)
	}
}
