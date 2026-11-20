// ClawEh
// License: MIT

package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/health"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/servicetoken"
)

// TestStartMCPServer_ReloadKeepsSessionTokens drives startMCPServer twice with
// the MCP host shut down in between, as handleConfigReload does: a session
// token issued before the reload still resolves on the rebuilt server, the
// store is the same one, and the service tokens are re-synced from disk.
func TestStartMCPServer_ReloadKeepsSessionTokens(t *testing.T) {
	home := t.TempDir()
	t.Setenv(global.EnvVarHome, home)
	cfg := config.DefaultConfig()
	cfg.Agents.BaseDir = t.TempDir()
	cfg.Agents.Defaults.Models = []string{"test-model"}
	cfg.Agents.List = []config.AgentConfig{{ID: "alice", Name: "Alice", Default: true}}
	cfg.MCPHost.Enabled = true
	cfg.MCPHost.Listen = "127.0.0.1:0"

	msgBus := bus.NewMessageBus()
	al, err := agent.NewAgentLoop(cfg, msgBus, providers.NewUnconfiguredProvider(), nil)
	if err != nil {
		t.Fatalf("NewAgentLoop: %v", err)
	}
	t.Cleanup(func() { al.Close(context.Background()) })

	const svcToken = "SVC-alice-plain"
	if err := servicetoken.Save(servicetoken.Path(cfg.DataDir()), map[string]string{"alice": svcToken}); err != nil {
		t.Fatalf("save service tokens: %v", err)
	}

	services := &gatewayServices{fatal: newFatalNotifier(nil)}
	if err := startMCPServer(cfg, al, msgBus, services); err != nil {
		t.Fatalf("first start: %v", err)
	}
	store := services.SessionTokens
	if store == nil || services.MCPServer.SessionTokens() != store {
		t.Fatal("the MCP server does not use the process-lifetime store")
	}
	tok := store.Issue("alice", "agent:alice:subagent:1", "/tmp/archive/alice")

	// The reload: the host is shut down, then rebuilt from the new config.
	first := services.MCPServer
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := startMCPServer(cfg, al, msgBus, services); err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() {
		if err := services.MCPServer.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	if services.MCPServer == first {
		t.Fatal("the MCP server was not rebuilt")
	}
	if services.SessionTokens != store || services.MCPServer.SessionTokens() != store {
		t.Fatal("the reload replaced the session-token store")
	}
	if _, ok := store.Resolve(tok); !ok {
		t.Fatal("a token issued before the reload no longer resolves")
	}
	if _, ok := store.Resolve(svcToken); !ok {
		t.Fatal("the service token was not re-synced after the reload")
	}
}

// TestStartMCPServer_TokensLoadedBeforeListening: the service tokens are in
// the store before the host binds its port, so no request can reach it while
// a valid token is still unknown. The port is taken, so the start stops at
// the bind: whatever is in the store then was loaded before it.
func TestStartMCPServer_TokensLoadedBeforeListening(t *testing.T) {
	home := t.TempDir()
	t.Setenv(global.EnvVarHome, home)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := ln.Close(); closeErr != nil {
			t.Errorf("close: %v", closeErr)
		}
	})

	cfg := config.DefaultConfig()
	cfg.Agents.BaseDir = t.TempDir()
	cfg.Agents.Defaults.Models = []string{"test-model"}
	cfg.Agents.List = []config.AgentConfig{{ID: "alice", Name: "Alice", Default: true}}
	cfg.MCPHost.Enabled = true
	cfg.MCPHost.Listen = ln.Addr().String()

	msgBus := bus.NewMessageBus()
	al, err := agent.NewAgentLoop(cfg, msgBus, providers.NewUnconfiguredProvider(), nil)
	if err != nil {
		t.Fatalf("NewAgentLoop: %v", err)
	}
	t.Cleanup(func() { al.Close(context.Background()) })

	const svcToken = "SVC-alice-plain"
	if err := servicetoken.Save(servicetoken.Path(cfg.DataDir()), map[string]string{"alice": svcToken}); err != nil {
		t.Fatalf("save service tokens: %v", err)
	}

	services := &gatewayServices{fatal: newFatalNotifier(nil)}
	if err := startMCPServer(cfg, al, msgBus, services); err == nil {
		if err := services.MCPServer.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		t.Fatal("the start succeeded on a port already in use")
	}
	if _, ok := services.SessionTokens.Resolve(svcToken); !ok {
		t.Fatal("the service token was not loaded before the host bound its port")
	}
}

// readyStatus is what /ready answers on the given health server.
func readyStatus(t *testing.T, hs *health.Server) int {
	t.Helper()
	mux := http.NewServeMux()
	hs.RegisterOnMux(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	return rec.Code
}

// TestRestartMCPHost_Readiness: after a reload /ready answers 200 only when
// the MCP host is back or is not enabled; a host that failed to restart
// leaves it at 503.
func TestRestartMCPHost_Readiness(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := taken.Close(); closeErr != nil {
			t.Errorf("close: %v", closeErr)
		}
	})

	tests := []struct {
		name    string
		enabled bool
		listen  string
		wantErr bool
		want    int
	}{
		{"host restarted", true, "127.0.0.1:0", false, http.StatusOK},
		{"host disabled", false, "127.0.0.1:0", false, http.StatusOK},
		{"host failed to restart", true, taken.Addr().String(), true, http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(global.EnvVarHome, t.TempDir())
			cfg := config.DefaultConfig()
			cfg.Agents.BaseDir = t.TempDir()
			cfg.Agents.Defaults.Models = []string{"test-model"}
			cfg.Agents.List = []config.AgentConfig{{ID: "alice", Name: "Alice", Default: true}}
			cfg.MCPHost.Enabled = tc.enabled
			cfg.MCPHost.Listen = tc.listen
			if got := cfg.MCPHostEffectivelyEnabled(); got != tc.enabled {
				t.Fatalf("MCPHostEffectivelyEnabled = %v, want %v", got, tc.enabled)
			}

			msgBus := bus.NewMessageBus()
			al, err := agent.NewAgentLoop(cfg, msgBus, providers.NewUnconfiguredProvider(), nil)
			if err != nil {
				t.Fatalf("NewAgentLoop: %v", err)
			}
			t.Cleanup(func() { al.Close(context.Background()) })

			// A reload starts from a fresh, not-ready health server.
			services := &gatewayServices{fatal: newFatalNotifier(nil), HealthServer: health.NewServer("127.0.0.1", 0)}
			if got := readyStatus(t, services.HealthServer); got != http.StatusServiceUnavailable {
				t.Fatalf("/ready before the restart = %d, want 503", got)
			}

			err = restartMCPHost(cfg, al, msgBus, services)
			if (err != nil) != tc.wantErr {
				t.Fatalf("restartMCPHost error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && services.MCPServer != nil {
				t.Cleanup(func() {
					if shutErr := services.MCPServer.Shutdown(context.Background()); shutErr != nil {
						t.Errorf("shutdown: %v", shutErr)
					}
				})
			}
			if got := readyStatus(t, services.HealthServer); got != tc.want {
				t.Fatalf("/ready after the restart = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestRestartMCPHost_ReadyAgainAfterFailedRestart: a reload whose MCP host
// could not start leaves /ready at 503, and the next reload that brings the
// host back answers 200 again, so the state is not permanent.
func TestRestartMCPHost_ReadyAgainAfterFailedRestart(t *testing.T) {
	t.Setenv(global.EnvVarHome, t.TempDir())
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_ = taken.Close()
		}
	})

	cfg := config.DefaultConfig()
	cfg.Agents.BaseDir = t.TempDir()
	cfg.Agents.Defaults.Models = []string{"test-model"}
	cfg.Agents.List = []config.AgentConfig{{ID: "alice", Name: "Alice", Default: true}}
	cfg.MCPHost.Enabled = true
	cfg.MCPHost.Listen = taken.Addr().String()

	msgBus := bus.NewMessageBus()
	al, err := agent.NewAgentLoop(cfg, msgBus, providers.NewUnconfiguredProvider(), nil)
	if err != nil {
		t.Fatalf("NewAgentLoop: %v", err)
	}
	t.Cleanup(func() { al.Close(context.Background()) })

	services := &gatewayServices{fatal: newFatalNotifier(nil), HealthServer: health.NewServer("127.0.0.1", 0)}
	if err := restartMCPHost(cfg, al, msgBus, services); err == nil {
		t.Fatal("the restart succeeded on a port already in use")
	}
	if got := readyStatus(t, services.HealthServer); got != http.StatusServiceUnavailable {
		t.Fatalf("/ready after the failed restart = %d, want 503", got)
	}

	// The next reload: what was stopping the host is gone. The services are
	// stopped first, as handleConfigReload does, which also covers a host
	// that never started.
	if err := taken.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	released = true
	stopAndCleanupServices(services, al, serviceShutdownTimeout, nil, true)
	if err := restartMCPHost(cfg, al, msgBus, services); err != nil {
		t.Fatalf("restart after the port was freed: %v", err)
	}
	t.Cleanup(func() {
		if shutErr := services.MCPServer.Shutdown(context.Background()); shutErr != nil {
			t.Errorf("shutdown: %v", shutErr)
		}
	})
	if got := readyStatus(t, services.HealthServer); got != http.StatusOK {
		t.Fatalf("/ready after the host came back = %d, want 200", got)
	}
}
