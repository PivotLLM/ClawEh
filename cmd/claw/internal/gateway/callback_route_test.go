package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/health"
)

// hitCallback POSTs to /api/reply/{token} via the external mux that
// health.Server's Handle propagates to, returning the response status.
//
// We use an empty body on purpose: the handler short-circuits with 400
// before dereferencing the AgentLoop, so the test can pass nil and still
// distinguish "route registered" (400) from "route missing" (404).
func hitCallback(t *testing.T, mux *http.ServeMux) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/reply/sometoken", bytes.NewReader(nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

// TestRegisterCallbackRoute_RouteIsRegistered verifies that calling
// RegisterCallbackRoute makes /api/reply/{token} reachable on the shared mux.
func TestRegisterCallbackRoute_RouteIsRegistered(t *testing.T) {
	server := health.NewServer("127.0.0.1", 0)
	mux := http.NewServeMux()
	server.RegisterOnMux(mux)

	RegisterCallbackRoute(server, nil)

	if got := hitCallback(t, mux); got != http.StatusBadRequest {
		t.Fatalf("after RegisterCallbackRoute: got status %d, want %d (empty-body 400 indicates route is registered)", got, http.StatusBadRequest)
	}
}

// TestCallbackRouteSurvivesServerRebuild is the regression test for the
// config-reload 404 bug: handleConfigReload → restartServices rebuilds the
// shared health.Server from scratch. If RegisterCallbackRoute is not called
// on the new server, every callback returns 404 even though tokens are still
// valid. This test simulates that lifecycle.
func TestCallbackRouteSurvivesServerRebuild(t *testing.T) {
	// Boot: shared server registers the callback route.
	bootServer := health.NewServer("127.0.0.1", 0)
	bootMux := http.NewServeMux()
	bootServer.RegisterOnMux(bootMux)
	RegisterCallbackRoute(bootServer, nil)
	if got := hitCallback(t, bootMux); got != http.StatusBadRequest {
		t.Fatalf("boot: got status %d, want %d", got, http.StatusBadRequest)
	}

	// Reload, step 1: a fresh server with NO callback registration documents
	// the bug — the route disappears and POSTs 404.
	freshServer := health.NewServer("127.0.0.1", 0)
	freshMux := http.NewServeMux()
	freshServer.RegisterOnMux(freshMux)
	if got := hitCallback(t, freshMux); got != http.StatusNotFound {
		t.Fatalf("fresh server without registration: got status %d, want %d (this status would indicate the bug is no longer reproducible — re-evaluate the test)", got, http.StatusNotFound)
	}

	// Reload, step 2: restartServices now calls RegisterCallbackRoute on the
	// rebuilt server, so the route returns.
	reloadedServer := health.NewServer("127.0.0.1", 0)
	reloadedMux := http.NewServeMux()
	reloadedServer.RegisterOnMux(reloadedMux)
	RegisterCallbackRoute(reloadedServer, nil)
	if got := hitCallback(t, reloadedMux); got != http.StatusBadRequest {
		t.Fatalf("reloaded server with registration: got status %d, want %d", got, http.StatusBadRequest)
	}
}
