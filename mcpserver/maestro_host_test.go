// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/mcpserver/acl"
	"github.com/PivotLLM/ClawEh/tools"
	toolsmaestro "github.com/PivotLLM/ClawEh/tools/maestro"
)

type hostFakeRunner struct{}

func (hostFakeRunner) RunSync(context.Context, string, string) (*global.SyncResult, error) {
	return &global.SyncResult{Content: "ok"}, nil
}

// TestHost_ServesRealMaestroSuiteInFull: the real Maestro suite, registered
// discovery-hidden exactly as the agent loop does when progressive discovery
// is on, is listed in full and dispatchable through the MCP host. This is the
// contract CLI providers rely on: the host never applies discovery.
func TestHost_ServesRealMaestroSuiteInFull(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{List: []config.AgentConfig{
		{ID: "penny", Maestro: &config.MaestroConfig{Enabled: true}},
	}}}
	built := tools.NamespacedProvider("maestro", toolsmaestro.GlobalProvider).Build(tools.ToolDeps{
		Cfg:       cfg,
		AgentCfg:  &cfg.Agents.List[0],
		AgentID:   "penny",
		Workspace: t.TempDir(),
		Spawn:     hostFakeRunner{},
	})
	if len(built) != 61 {
		t.Fatalf("built %d maestro tools, want 61 (64 minus the 3 LLM tools)", len(built))
	}
	reg := tools.NewToolRegistry()
	for _, tl := range built {
		reg.RegisterSuiteHidden(tl)
	}

	regs := map[string]*tools.ToolRegistry{"penny": reg}
	st, tok := seedSessionToken("penny")
	resolver := resolverFor(regs)

	srv := server.NewMCPServer("t", "0")
	addToolsToServer(srv, bearerAuthMode, regs, []string{"*"}, st, resolver, nil, acl.Default, nil, nil, nil)
	listed := srv.ListTools()
	n := 0
	for name := range listed {
		if strings.HasPrefix(name, "maestro_") {
			n++
		}
	}
	if n != 61 {
		t.Fatalf("host lists %d maestro tools, want all 61", n)
	}
	for _, absent := range []string{"maestro_llm_list", "maestro_llm_dispatch", "maestro_llm_test"} {
		if _, ok := listed[absent]; ok {
			t.Errorf("%s must not be served", absent)
		}
	}

	out, isErr := dispatchToolCall(context.Background(), "maestro_health",
		map[string]any{"session_token": tok}, st, resolver, nil, acl.Default, nil, nil)
	if isErr || !strings.Contains(out, `"dispatch":"host"`) {
		t.Fatalf("host dispatch of maestro_health: isErr=%v out=%s", isErr, out)
	}
}
