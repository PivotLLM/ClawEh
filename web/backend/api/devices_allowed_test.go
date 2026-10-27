// ClawEh
// License: MIT

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// POST /api/devices/{id}/agents stores the device's allowed agents
// (normalised), GET /api/devices returns them with agent_allowed, an invalid
// id is a 400 that leaves the stored set alone, and [] allows every agent.
func TestDeviceAllowedAgents_RoundTrip(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	pairTestDevice(t, h, "dev1")
	mux := http.NewServeMux()
	h.registerDeviceRoutes(mux)

	post := func(id, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices/"+id+"/agents", strings.NewReader(body)))
		return rec
	}

	// No restriction: an array (not null), and agent_allowed true.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/devices", nil))
	if !strings.Contains(rec.Body.String(), `"allowed_agents":[]`) || !strings.Contains(rec.Body.String(), `"agent_allowed":true`) {
		t.Fatalf("before: body %s, want allowed_agents [] and agent_allowed true", rec.Body.String())
	}

	if rec := post("dev1", `{"allowed_agents":["Bob","alice","bob"]}`); rec.Code != http.StatusOK {
		t.Fatalf("set: status = %d: %s", rec.Code, rec.Body.String())
	}
	devs := listDevices(t, mux)
	if len(devs) != 1 || !slices.Equal(devs[0].AllowedAgents, []string{"bob", "alice"}) || !devs[0].AgentAllowed {
		t.Fatalf("after set: devices = %+v, want allowed_agents [bob alice], agent_allowed true", devs)
	}

	for _, body := range []string{`{"allowed_agents":["bob","not valid"]}`, `{"allowed_agents":[""]}`, `{"allowed_agents":["-bob"]}`} {
		rec := post("dev1", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400: %s", body, rec.Code, rec.Body.String())
		}
		var resp map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || !strings.HasSuffix(resp["error"], "is not a valid agent id.") {
			t.Fatalf("%s: body %s, want an invalid agent id message", body, rec.Body.String())
		}
	}
	if devs = listDevices(t, mux); !slices.Equal(devs[0].AllowedAgents, []string{"bob", "alice"}) {
		t.Fatalf("a refused set changed the stored one: %v", devs[0].AllowedAgents)
	}

	if rec := post("missing", `{"allowed_agents":["bob"]}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown device: status = %d, want 404", rec.Code)
	}
	if rec := post("dev1", `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid body: status = %d, want 400", rec.Code)
	}
	// Only an explicit [] allows every agent; a missing or null field is refused.
	for _, body := range []string{`{}`, `{"allowed_agents":null}`} {
		rec := post("dev1", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"allowed_agents is required."`) {
			t.Fatalf("%s: status = %d body %s, want 400 allowed_agents is required.", body, rec.Code, rec.Body.String())
		}
	}
	if devs = listDevices(t, mux); !slices.Equal(devs[0].AllowedAgents, []string{"bob", "alice"}) {
		t.Fatalf("a missing field changed the stored set: %v", devs[0].AllowedAgents)
	}

	if rec := post("dev1", `{"allowed_agents":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("clear: status = %d: %s", rec.Code, rec.Body.String())
	}
	if devs = listDevices(t, mux); devs[0].AllowedAgents == nil || len(devs[0].AllowedAgents) != 0 {
		t.Fatalf("after clear: allowed_agents = %#v, want []", devs[0].AllowedAgents)
	}
}

// agent_allowed is false when the device's assigned agent is outside its
// allowed set, and true when it is inside.
func TestDeviceList_AgentAllowed(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	pairTestDevice(t, h, "dev1")
	ctx := context.Background()
	store, _, err := h.openDeviceStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeviceAgent(ctx, "dev1", "alice"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.registerDeviceRoutes(mux)

	for _, tc := range []struct {
		allowed []string
		want    bool
	}{
		{[]string{"bob"}, false},
		{[]string{"bob", "Alice"}, true},
		{nil, true},
	} {
		if err := store.SetDeviceAllowedAgents(ctx, "dev1", tc.allowed); err != nil {
			t.Fatal(err)
		}
		if devs := listDevices(t, mux); len(devs) != 1 || devs[0].AgentAllowed != tc.want {
			t.Fatalf("allowed %v: devices = %+v, want agent_allowed %v", tc.allowed, devs, tc.want)
		}
	}
}
