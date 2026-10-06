// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package tools

import "github.com/PivotLLM/ClawEh/global"

// ParameterSchemaKey is the global.Parameter Metadata key whose value (a
// JSON Schema object, map[string]any) replaces the schema generated for
// that parameter. toolspec renders a parameter as a flat type, so a
// structured argument (the forum configuration) carries its full nested
// schema here; DefinitionSchema substitutes it, and since every exported
// schema (ToolToSchema for models, the MCP host's tools/list) is built
// from Tool.Parameters, both publish it. The parameter's Description is
// used when the schema has none. OpenObjectProperties stays the fallback
// for object parameters without one.
const ParameterSchemaKey = "json_schema"

// DefinitionSchema is def.Schema() with every parameter that carries a
// ParameterSchemaKey schema replaced by it. A RawSchema is returned as is.
// The returned map may share the substituted schemas: callers copy before
// changing anything, as OpenObjectProperties does.
func DefinitionSchema(def global.ToolDefinition) map[string]any {
	schema := def.Schema()
	if def.RawSchema != nil {
		return schema
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return schema
	}
	for _, p := range def.Parameters {
		custom, ok := p.Metadata[ParameterSchemaKey].(map[string]any)
		if !ok {
			continue
		}
		prop := copyMap(custom)
		if _, has := prop["description"]; !has && p.Description != "" {
			prop["description"] = p.Description
		}
		props[p.Name] = prop
	}
	return schema
}
