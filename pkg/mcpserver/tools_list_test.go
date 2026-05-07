// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/agenttoken"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// listedToolNames sends a tools/list JSON-RPC message through the MCP
// server and returns the sorted list of tool names from the response.
func listedToolNames(t *testing.T, srv *MCPServer, params map[string]any) []string {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
		"params":  params,
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := srv.srv.HandleMessage(context.Background(), body)
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		Error  any `json:"error,omitempty"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode tools/list response: %v\nbody: %s", err, raw)
	}
	if parsed.Error != nil {
		t.Fatalf("tools/list returned error: %v\nbody: %s", parsed.Error, raw)
	}

	names := make([]string, 0, len(parsed.Result.Tools))
	for _, tl := range parsed.Result.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	return names
}

// TestToolsList_ReturnsFullCatalogueRegardlessOfToken pins the contract
// that tool enumeration is global: tools/list returns the same union
// catalogue with no token, an unknown token, or a valid token in params.
func TestToolsList_ReturnsFullCatalogueRegardlessOfToken(t *testing.T) {
	aliceRF := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("a")}
	aliceWF := &mockTool{name: "write_file", params: map[string]any{}, result: tools.NewToolResult("a")}
	bobRF := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("b")}
	bobLD := &mockTool{name: "list_dir", params: map[string]any{}, result: tools.NewToolResult("b")}

	tm := agenttoken.NewManager()
	aliceTok := tm.Issue("alice")
	tm.Issue("bob")

	srv, err := New(
		WithAgentRegistries(map[string]*tools.ToolRegistry{
			"alice": newRegistryWith(aliceRF, aliceWF),
			"bob":   newRegistryWith(bobRF, bobLD),
		}),
		WithAgentTokens(tm),
		WithAllowlist([]string{"*"}),
	)
	if err != nil {
		t.Fatal(err)
	}

	expect := []string{"list_dir", "read_file", "write_file"}

	cases := []struct {
		name   string
		params map[string]any
	}{
		{"no token", map[string]any{}},
		{"unknown token", map[string]any{"agent_token": agenttoken.Prefix + strings.Repeat("a", 64)}},
		{"valid token", map[string]any{"agent_token": aliceTok}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := listedToolNames(t, srv, c.params)
			if !equalStrings(got, expect) {
				t.Errorf("tools/list mismatch: want %v, got %v", expect, got)
			}
		})
	}
}

// TestToolsList_NeverEmitsToolCallLogs confirms tools/list does not run
// through the dispatch path and so emits none of the tool-call WARN/INFO
// log entries.
func TestToolsList_NeverEmitsToolCallLogs(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	rf := &mockTool{name: "read_file", params: map[string]any{}, result: tools.NewToolResult("ok")}
	tm := agenttoken.NewManager()
	srv, err := New(
		WithAgentRegistries(map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}),
		WithAgentTokens(tm),
		WithAllowlist([]string{"*"}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_ = listedToolNames(t, srv, map[string]any{})
	_ = listedToolNames(t, srv, map[string]any{"agent_token": "AGTbogus"})

	logs := buf.String()
	for _, marker := range []string{
		`"message":"MCP tool authorized"`,
		`"message":"MCP token rejected"`,
		`"message":"MCP token rejected: subagent sentinel"`,
		`"message":"MCP token rejected: no registry for agent"`,
		`"message":"MCP tool denied"`,
	} {
		if strings.Contains(logs, marker) {
			t.Errorf("unexpected dispatch log on tools/list: %s\nfull logs:\n%s", marker, logs)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
