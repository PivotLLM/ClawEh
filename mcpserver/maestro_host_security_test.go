// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/mcpserver/acl"
	"github.com/PivotLLM/ClawEh/tools"
	toolsmaestro "github.com/PivotLLM/ClawEh/tools/maestro"
)

func maestroRegistryFor(t *testing.T, agentID string) *tools.ToolRegistry {
	t.Helper()
	cfg := &config.Config{Agents: config.AgentsConfig{List: []config.AgentConfig{
		{ID: agentID, Maestro: &config.MaestroConfig{Enabled: true}},
	}}}
	built := tools.NamespacedProvider("maestro", toolsmaestro.GlobalProvider).Build(tools.ToolDeps{
		Cfg: cfg, AgentCfg: &cfg.Agents.List[0], AgentID: agentID, Workspace: t.TempDir(), Spawn: hostFakeRunner{},
	})
	reg := tools.NewToolRegistry()
	for _, tl := range built {
		reg.RegisterSuiteHidden(tl)
	}
	return reg
}

// TestHost_MaestroToolsGatedPerAgent: a token for an agent without Maestro
// cannot execute another agent's Maestro tools, even though the shared
// catalogue advertises them.
func TestHost_MaestroToolsGatedPerAgent(t *testing.T) {
	regs := map[string]*tools.ToolRegistry{
		"penny": maestroRegistryFor(t, "penny"),
		"bob":   tools.NewToolRegistry(), // no Maestro
	}
	st := newSessionTokenStore()
	pennyTok := st.Issue("penny", "test:penny:main", "/tmp/archive/penny")
	bobTok := st.Issue("bob", "test:bob:main", "/tmp/archive/bob")
	resolver := resolverFor(regs)

	out, isErr := dispatchToolCall(context.Background(), "maestro_health",
		map[string]any{"session_token": pennyTok}, st, resolver, nil, acl.Default, nil, nil)
	if isErr || !strings.Contains(out, `"dispatch":"host"`) {
		t.Fatalf("penny (maestro on) must dispatch: isErr=%v out=%s", isErr, out)
	}

	out, isErr = dispatchToolCall(context.Background(), "maestro_health",
		map[string]any{"session_token": bobTok}, st, resolver, nil, acl.Default, nil, nil)
	if !isErr {
		t.Fatalf("bob (maestro off) executed a Maestro tool: %s", out)
	}
	if strings.Contains(out, `"dispatch":"host"`) {
		t.Error("bob received penny's Maestro output")
	}
}

// TestHost_MaestroToolRejectsBadTokens: no token, an unknown token and a
// malformed token are all refused before any Maestro code runs.
func TestHost_MaestroToolRejectsBadTokens(t *testing.T) {
	regs := map[string]*tools.ToolRegistry{"penny": maestroRegistryFor(t, "penny")}
	st, _ := seedSessionToken("penny")
	resolver := resolverFor(regs)
	for name, tok := range map[string]any{"missing": nil, "unknown": "SST" + strings.Repeat("0", 64), "malformed": "not-a-token"} {
		args := map[string]any{}
		if tok != nil {
			args["session_token"] = tok
		}
		out, isErr := dispatchToolCall(context.Background(), "maestro_health", args, st, resolver, nil, acl.Default, nil, nil)
		if !isErr {
			t.Errorf("%s token: Maestro tool executed: %s", name, out)
		}
		if strings.Contains(out, `"dispatch":"host"`) {
			t.Errorf("%s token: Maestro output leaked", name)
		}
	}
}
