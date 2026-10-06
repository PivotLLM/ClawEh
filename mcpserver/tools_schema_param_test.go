// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

type nestedSchemaProvider struct{}

func (nestedSchemaProvider) RegisterTools(global.Deps) []global.ToolDefinition {
	return []global.ToolDefinition{{
		Name:        "set",
		Description: "test",
		Handler:     func(*global.ToolCall) (*global.Result, error) { return &global.Result{}, nil },
		Parameters: []global.Parameter{
			{
				Name: "config", Type: "object", Required: true, Description: "The configuration",
				Metadata: map[string]any{tools.ParameterSchemaKey: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties":           map[string]any{"name": map[string]any{"type": "string"}},
				}},
			},
		},
	}}
}

// tools/list publishes a parameter's ParameterSchemaKey schema in place of
// the generated one.
func TestToolsList_PublishesParameterSchema(t *testing.T) {
	built := tools.NamespacedProvider("demo", nestedSchemaProvider{}).Build(tools.ToolDeps{})
	srv, err := New(
		WithAgentRegistries(map[string]*tools.ToolRegistry{"alice": newRegistryWith(built...)}),
		WithAllowlist([]string{"*"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(srv.srv.HandleMessage(context.Background(), body))
	if err != nil {
		t.Fatal(err)
	}
	want := `"config":{"additionalProperties":false,"description":"The configuration","properties":{"name":{"type":"string"}},"type":"object"}`
	if !strings.Contains(string(raw), want) {
		t.Errorf("tools/list = %s\nwant it to contain %s", raw, want)
	}
}
