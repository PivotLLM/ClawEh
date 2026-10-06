// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/global"
)

// nestedSchema is a structured argument's full schema.
var nestedSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties":           map[string]any{"name": map[string]any{"type": "string"}},
}

type schemaParamProvider struct{}

func (schemaParamProvider) RegisterTools(global.Deps) []global.ToolDefinition {
	return []global.ToolDefinition{{
		Name:        "set",
		Description: "test",
		Handler:     func(*global.ToolCall) (*global.Result, error) { return &global.Result{}, nil },
		Parameters: []global.Parameter{
			{Name: "id", Type: "string", Required: true},
			{
				Name: "config", Type: "object", Required: true, Description: "The configuration",
				Metadata: map[string]any{ParameterSchemaKey: nestedSchema},
			},
			{Name: "extra", Type: "object"},
		},
	}}
}

// A parameter carrying a ParameterSchemaKey schema is published with it,
// keeping its description; other parameters are unchanged, and a free-form
// object is still opened.
func TestToolToSchemaSubstitutesParameterSchema(t *testing.T) {
	built := NamespacedProvider("demo", schemaParamProvider{}).Build(ToolDeps{})
	if len(built) != 1 {
		t.Fatalf("built %d tools", len(built))
	}
	got, err := json.Marshal(ToolToSchema(built[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"config":{"additionalProperties":false,"description":"The configuration","properties":{"name":{"type":"string"}},"type":"object"}`,
		`"extra":{"additionalProperties":true,"type":"object"}`,
		`"id":{"type":"string"}`,
		`"required":["id","config"]`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("schema = %s\nwant it to contain %s", got, want)
		}
	}
	if _, has := nestedSchema["description"]; has {
		t.Error("the parameter's schema was modified")
	}
}
