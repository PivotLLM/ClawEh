package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/internal/audit"
)

// newRestartHandler returns a handler with the restart hook and the
// service-manager check injected, and a counter of hook calls.
func newRestartHandler(t *testing.T, managed bool) (*Handler, *int) {
	t.Helper()
	h := NewHandler(setupTestEnv(t))
	calls := 0
	h.SetRestart(func() { calls++ })
	h.serviceManagedFn = func() bool { return managed }
	return h, &calls
}

func postRestart(h *Handler) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/system/restart", nil)
	req = req.WithContext(audit.WithActor(req.Context(), "alice"))
	req.RemoteAddr = "192.168.1.9:4242"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Under a service manager the request is accepted, recorded and acted on.
func TestSystemRestart_UnderServiceManager(t *testing.T) {
	store := initTestAudit(t)
	h, calls := newRestartHandler(t, true)

	rec := postRestart(h)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "restarting" {
		t.Errorf("body = %v", body)
	}
	if *calls != 1 {
		t.Errorf("restart hook called %d times, want 1", *calls)
	}

	flushAudit(t)
	events, err := store.Query(context.Background(), audit.Filter{Kind: audit.KindRestart})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("restart audit rows = %d, want 1", len(events))
	}
	e := events[0]
	if e.Actor != "alice" || e.Sender != "192.168.1.9" || e.Outcome != audit.OutcomeOK {
		t.Errorf("event = %+v", e)
	}
}

// Without a service manager nothing would bring the process back: refuse,
// change nothing, record nothing.
func TestSystemRestart_NotAService(t *testing.T) {
	store := initTestAudit(t)
	h, calls := newRestartHandler(t, false)

	rec := postRestart(h)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "not running as a service; restart ClawEh by hand" {
		t.Errorf("error = %q", body["error"])
	}
	if *calls != 0 {
		t.Errorf("restart hook called %d times, want 0", *calls)
	}

	flushAudit(t)
	events, err := store.Query(context.Background(), audit.Filter{Kind: audit.KindRestart})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("restart audit rows = %d, want 0", len(events))
	}
}

// A handler the gateway has not wired (tests, tools) cannot restart anything.
func TestSystemRestart_NoHook(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	h.serviceManagedFn = func() bool { return true }
	rec := postRestart(h)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}

// The real check reads systemd's INVOCATION_ID.
func TestUnderServiceManager(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")
	if underServiceManager() {
		t.Error("no INVOCATION_ID but reported as service-managed")
	}
	t.Setenv("INVOCATION_ID", "6ba7b8109dad11d180b400c04fd430c8")
	if !underServiceManager() {
		t.Error("INVOCATION_ID set but not reported as service-managed")
	}
}
