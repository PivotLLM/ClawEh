package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/agent"
	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/health"
	"github.com/PivotLLM/ClawEh/pkg/providers"
	webserver "github.com/PivotLLM/ClawEh/web/backend"
)

// hitCallback POSTs to /api/message/{token} via the external mux that
// health.Server's Handle propagates to, returning the response status.
//
// We use an empty body on purpose: the handler short-circuits with 400
// before dereferencing the AgentLoop, so the test can pass nil and still
// distinguish "route registered" (400) from "route missing" (404).
func hitCallback(t *testing.T, mux *http.ServeMux) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/message/sometoken", bytes.NewReader(nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

// TestRegisterMessageRoute_RouteIsRegistered verifies that calling
// RegisterMessageRoute makes /api/message/{token} reachable on the shared mux.
func TestRegisterMessageRoute_RouteIsRegistered(t *testing.T) {
	server := health.NewServer("127.0.0.1", 0)
	mux := http.NewServeMux()
	server.RegisterOnMux(mux)

	RegisterMessageRoute(server, nil)

	if got := hitCallback(t, mux); got != http.StatusBadRequest {
		t.Fatalf("after RegisterMessageRoute: got status %d, want %d (empty-body 400 indicates route is registered)", got, http.StatusBadRequest)
	}
}

// TestCallbackRouteSurvivesServerRebuild is the regression test for the
// config-reload 404 bug: handleConfigReload → restartServices rebuilds the
// shared health.Server from scratch. If RegisterMessageRoute is not called
// on the new server, every callback returns 404 even though tokens are still
// valid. This test simulates that lifecycle.
func TestCallbackRouteSurvivesServerRebuild(t *testing.T) {
	// Boot: shared server registers the callback route.
	bootServer := health.NewServer("127.0.0.1", 0)
	bootMux := http.NewServeMux()
	bootServer.RegisterOnMux(bootMux)
	RegisterMessageRoute(bootServer, nil)
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

	// Reload, step 2: restartServices now calls RegisterMessageRoute on the
	// rebuilt server, so the route returns.
	reloadedServer := health.NewServer("127.0.0.1", 0)
	reloadedMux := http.NewServeMux()
	reloadedServer.RegisterOnMux(reloadedMux)
	RegisterMessageRoute(reloadedServer, nil)
	if got := hitCallback(t, reloadedMux); got != http.StatusBadRequest {
		t.Fatalf("reloaded server with registration: got status %d, want %d", got, http.StatusBadRequest)
	}
}

// fakeChannelManager captures the mux that the reload-path seam rebuilds, so
// the test can assert that the callback route survived the rebuild without
// standing up a real channels.Manager. Mirrors what channels.Manager.RegisterHandlers
// does to the shared mux: register the health server on it (which also stores
// it as externalMux so subsequent Handle calls register there too).
type fakeChannelManager struct {
	mux *http.ServeMux
}

func (f *fakeChannelManager) RegisterHandlers(mux *http.ServeMux, hs *health.Server) {
	f.mux = mux
	if hs != nil {
		hs.RegisterOnMux(mux)
	}
}

// fakeMuxSwapper captures the mux passed to SetMux so tests can assert that
// rebuildSharedHTTPServer installed the freshly-built mux on the httpHost.
type fakeMuxSwapper struct {
	mux *http.ServeMux
}

func (f *fakeMuxSwapper) SetMux(mux *http.ServeMux) { f.mux = mux }

// TestRebuildSharedHTTPServer_RegistersCallbackRoute hardens the regression
// guard against the e32731eb mutation: directly exercises the seam that
// restartServices uses to rebuild the shared HTTP server, and asserts that
// the callback route is reachable on the rebuilt mux. If the
// RegisterMessageRoute call is removed from rebuildSharedHTTPServer (or any
// future refactor stops calling it from the reload path), this test fails
// because POST /api/message/{token} returns 404 instead of 400.
func TestRebuildSharedHTTPServer_RegistersCallbackRoute(t *testing.T) {
	services := &gatewayServices{}
	fake := &fakeChannelManager{}
	swap := &fakeMuxSwapper{}

	rebuildSharedHTTPServer(services, "127.0.0.1", 0, fake, swap, nil)

	if services.HealthServer == nil {
		t.Fatal("rebuildSharedHTTPServer did not assign services.HealthServer")
	}
	if fake.mux == nil {
		t.Fatal("rebuildSharedHTTPServer did not invoke RegisterHandlers on the channel manager")
	}
	if swap.mux == nil {
		t.Fatal("rebuildSharedHTTPServer did not swap the rebuilt mux into the httpHost")
	}
	if swap.mux != fake.mux {
		t.Fatal("rebuildSharedHTTPServer installed a different mux on the httpHost than the one passed to RegisterHandlers")
	}
	if got := hitCallback(t, fake.mux); got != http.StatusBadRequest {
		t.Fatalf("after rebuildSharedHTTPServer: got %d, want %d (404 means the callback route was dropped on reload — the production bug fixed in e32731eb)", got, http.StatusBadRequest)
	}
}

// TestMessageRoute_RateLimited429 exercises the full route: a named token
// flooded past its per-token limit is rejected with 429 and a Retry-After
// header, while requests within the limit are accepted (202).
func TestMessageRoute_RateLimited429(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: t.TempDir(),
			Defaults: config.AgentDefaults{
				Models:            []string{"test-model"},
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
			List: []config.AgentConfig{{ID: "main", Name: "Main", Default: true}},
		},
	}
	al := agent.NewAgentLoop(cfg, bus.NewMessageBus(), providers.NewUnconfiguredProvider(), nil)

	tok, err := al.CreateMessageToken("main", "gps")
	if err != nil {
		t.Fatalf("CreateMessageToken: %v", err)
	}
	if !al.UpdateMessageToken("main", tok.ID, 1, 15) { // limit 1/min
		t.Fatal("UpdateMessageToken returned false")
	}

	server := health.NewServer("127.0.0.1", 0)
	mux := http.NewServeMux()
	server.RegisterOnMux(mux)
	RegisterMessageRoute(server, al)

	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/message/"+tok.Token,
			strings.NewReader("hello"))
		mux.ServeHTTP(rec, req)
		return rec
	}

	// First request is within the 1/min limit. Delivery fails (no default
	// channel) with 422, which still proves the token passed the rate check —
	// a 429 would mean it was blocked before delivery.
	if rec := post(); rec.Code == http.StatusTooManyRequests {
		t.Fatalf("first request got 429, want it to pass the rate check")
	}
	// Second request trips the limit → 429 with a Retry-After header.
	rec := post()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 response missing Retry-After header")
	}
}

// hitGET issues a GET against the supplied mux and returns the recorder so
// callers can assert both status code and body content.
func hitGET(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestRebuildSharedHTTPServer_MergedBinaryRoutesSurviveReload exercises the
// reload seam end-to-end for the merged claw binary: it primes a
// gatewayServices with a real webserver.Server, calls rebuildSharedHTTPServer
// (the same path that handleConfigReload → restartServices takes on a
// config reload), and asserts that every route the merged binary needs to
// keep alive on reload is reachable on the rebuilt mux.
//
// The point is to guard against a future refactor that "rebuilds" the shared
// mux on reload but forgets to re-register one of:
//
//   - POST /api/message/{token} — the canonical callback-404 regression (the
//     gateway-side bug fixed in e32731eb).
//   - GET /api/gateway/logs — a representative WebUI API route; if missing
//     the WebUI loses backend connectivity for log streaming.
//   - GET / — the embedded SPA fallback; if missing the WebUI 404s.
//
// All three live on the same mux as the channel webhook handlers, and the
// reload code is the only place that constructs that mux. If reload stops
// re-registering them, the WebUI silently breaks until the user restarts the
// process — exactly the failure mode this test exists to catch.
func TestRebuildSharedHTTPServer_MergedBinaryRoutesSurviveReload(t *testing.T) {
	// Use a temp config file so the api.Handler has somewhere to read/write
	// without polluting the developer's real ~/.claw/config.json.
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	services := &gatewayServices{
		WebServer: webserver.New(webserver.Options{
			ConfigPath: configPath,
			ListenPort: cfg.Gateway.Port,
		}),
	}
	fake := &fakeChannelManager{}
	swap := &fakeMuxSwapper{}

	rebuildSharedHTTPServer(services, "127.0.0.1", 0, fake, swap, nil)

	if fake.mux == nil {
		t.Fatal("rebuildSharedHTTPServer did not invoke RegisterHandlers on the channel manager")
	}

	// /api/message/{token} — empty body short-circuits to 400, proving the
	// route is registered (404 would mean the merge dropped it).
	if got := hitCallback(t, fake.mux); got != http.StatusBadRequest {
		t.Fatalf("POST /api/message/{token}: got %d, want %d (route missing — reload dropped the callback handler)", got, http.StatusBadRequest)
	}

	// /api/gateway/logs — a representative WebUI API route surviving reload.
	logsRec := hitGET(t, fake.mux, "/api/gateway/logs")
	if logsRec.Code != http.StatusOK {
		t.Fatalf("GET /api/gateway/logs: got %d, want %d (route missing — reload dropped the WebUI API)", logsRec.Code, http.StatusOK)
	}

	// GET / — the embedded SPA fallback handler. We expect 200 (the embed
	// hands back the index page even when dist is empty — the .gitkeep
	// placeholder is served as a directory listing) or, if the dist
	// directory truly has no index, a redirect/404 that nonetheless proves
	// the handler is mounted (not the ServeMux's bare-route 404).
	rootRec := hitGET(t, fake.mux, "/")
	// In a freshly-built repo where the frontend bundle has not been built,
	// dist only contains .gitkeep. The handler still responds (it serves
	// the directory or 404s from the FileServer), but never returns the
	// default ServeMux 404 page. We assert the response was produced by
	// our handler by checking the Content-Type header — an unmounted route
	// would emit "text/plain; charset=utf-8" with body "404 page not
	// found".
	if rootRec.Code == http.StatusNotFound {
		body := rootRec.Body.String()
		if body == "404 page not found\n" {
			t.Fatalf("GET / returned the default ServeMux 404 — the SPA fallback handler is not mounted")
		}
	}
}
