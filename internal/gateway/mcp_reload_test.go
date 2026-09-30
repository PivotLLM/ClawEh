// ClawEh
// License: MIT

package gateway

import (
	"context"
	"testing"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
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
