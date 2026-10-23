package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// A free-form object parameter is declared open; an object with members,
// one that already says, and other types are left alone, and the input is
// not modified.
func TestOpenObjectProperties(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"changes": map[string]any{"type": "object", "description": "a merge patch"},
			"closed":  map[string]any{"type": "object", "additionalProperties": false},
			"nested": map[string]any{"type": "object", "properties": map[string]any{
				"args": map[string]any{"type": "object"},
				"name": map[string]any{"type": "string"},
			}},
			"id": map[string]any{"type": "string"},
		},
	}
	got := OpenObjectProperties(in)
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"changes": map[string]any{"type": "object", "description": "a merge patch", "additionalProperties": true},
			"closed":  map[string]any{"type": "object", "additionalProperties": false},
			"nested": map[string]any{"type": "object", "properties": map[string]any{
				"args": map[string]any{"type": "object", "additionalProperties": true},
				"name": map[string]any{"type": "string"},
			}},
			"id": map[string]any{"type": "string"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OpenObjectProperties =\n%v\nwant\n%v", got, want)
	}
	if before, err := json.Marshal(in); err != nil || strings.Contains(string(before), "additionalProperties\":true") {
		t.Errorf("the input schema was modified: %s (%v)", before, err)
	}
	plain := map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}}
	if got := OpenObjectProperties(plain); !reflect.DeepEqual(got, plain) {
		t.Errorf("a schema without object properties changed: %v", got)
	}
}

// Every schema a model or an MCP client is given goes through it.
func TestToolToSchemaOpensObjectProperties(t *testing.T) {
	tool := &schemaTool{params: map[string]any{"type": "object", "properties": map[string]any{
		"config": map[string]any{"type": "object"},
	}}}
	got, err := json.Marshal(ToolToSchema(tool))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"config":{"additionalProperties":true,"type":"object"}`) {
		t.Errorf("schema = %s, want config open", got)
	}
}

type schemaTool struct{ params map[string]any }

func (s *schemaTool) Name() string               { return "schema_tool" }
func (s *schemaTool) Description() string        { return "test" }
func (s *schemaTool) Parameters() map[string]any { return s.params }
func (s *schemaTool) Execute(_ context.Context, _ map[string]any) *ToolResult {
	return nil
}
