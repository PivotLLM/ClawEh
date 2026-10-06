// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// configFieldKeys lists every "<GoType>.<json name>" field reachable from
// Config, and the ones holding raw JSON.
func configFieldKeys() (all, raw []string) {
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		for name, ft := range structFields(t) {
			key := t.Name() + "." + name
			all = append(all, key)
			if ft == rawMessageType || (ft.Kind() == reflect.Map && ft.Elem() == rawMessageType) {
				raw = append(raw, key)
			}
			walk(ft)
		}
	}
	walk(reflect.TypeFor[Config]())
	sort.Strings(all)
	sort.Strings(raw)
	return all, raw
}

// Every configuration field has a description, and every description names
// a field: a field added to the Go types without one, or a description left
// behind by a removed field, fails here.
func TestConfigSchemaDocsMatchTypes(t *testing.T) {
	fields, raw := configFieldKeys()
	for _, key := range fields {
		if fieldDocs[key].desc == "" {
			t.Errorf("config field %s has no description in fieldDocs", key)
		}
	}
	for key := range fieldDocs {
		if !slices.Contains(fields, key) {
			t.Errorf("fieldDocs has %s, which is not a config field", key)
		}
	}
	rawKeys := make([]string, 0, len(rawSchemas))
	for key := range rawSchemas {
		rawKeys = append(rawKeys, key)
	}
	sort.Strings(rawKeys)
	if !slices.Equal(raw, rawKeys) {
		t.Errorf("raw JSON fields %v, rawSchemas %v", raw, rawKeys)
	}
}

// The published schemas have exactly the fields of the Go types, at every
// depth, and are closed wherever Decode refuses unknown fields.
func TestConfigSchemaPropertiesMatchTypes(t *testing.T) {
	for name, schema := range map[string]map[string]any{"config": ConfigSchema(), "patch": PatchSchema()} {
		t.Run(name, func(t *testing.T) {
			compareSchema(t, "", reflect.TypeFor[Config](), schema)
		})
	}
}

func compareSchema(t *testing.T, path string, typ reflect.Type, s map[string]any) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		if s["additionalProperties"] != false {
			t.Errorf("%s: object is not closed", path)
		}
		props, ok := s["properties"].(map[string]any)
		if !ok {
			t.Errorf("%s: object without properties", path)
			return
		}
		fields := structFields(typ)
		for name, ft := range fields {
			p, ok := props[name].(map[string]any)
			if !ok {
				t.Errorf("%s.%s: field missing from the schema", path, name)
				continue
			}
			compareSchema(t, path+"."+name, ft, p)
		}
		for name := range props {
			if _, ok := fields[name]; !ok {
				t.Errorf("%s.%s: in the schema but not a field", path, name)
			}
		}
	case reflect.Map:
		if typ.Elem() == rawMessageType {
			return
		}
		value, ok := s["additionalProperties"].(map[string]any)
		if !ok {
			t.Errorf("%s: map without a value schema", path)
			return
		}
		compareSchema(t, path+".*", typ.Elem(), value)
	case reflect.Slice:
		if typ == rawMessageType {
			return
		}
		items, ok := s["items"].(map[string]any)
		if !ok {
			t.Errorf("%s: array without an item schema", path)
			return
		}
		compareSchema(t, path+"[]", typ.Elem(), items)
	default: // a scalar: nothing nested to compare
	}
}

// The schemas keep to what every function-calling provider accepts.
func TestConfigSchemaPortable(t *testing.T) {
	for name, schema := range map[string]map[string]any{"config": ConfigSchema(), "patch": PatchSchema()} {
		var walk func(path string, v map[string]any)
		walk = func(path string, v map[string]any) {
			for _, k := range []string{"$ref", "$defs", "definitions", "oneOf", "anyOf", "allOf", "not", "pattern", "format", "if", "const"} {
				if _, ok := v[k]; ok {
					t.Errorf("%s %s: uses %s", name, path, k)
				}
			}
			if _, ok := v["enum"]; ok {
				if typ := v["type"]; typ != "string" && !reflect.DeepEqual(typ, []string{"string", "null"}) {
					t.Errorf("%s %s: enum on type %v", name, path, typ)
				}
			}
			if props, ok := v["properties"].(map[string]any); ok {
				for k, child := range props {
					if c, ok := child.(map[string]any); ok {
						walk(path+"/"+k, c)
					} else {
						t.Errorf("%s %s/%s: not a schema object", name, path, k)
					}
				}
			}
			if child, ok := v["additionalProperties"].(map[string]any); ok {
				walk(path+"/*", child)
			}
			if child, ok := v["items"].(map[string]any); ok {
				walk(path+"[]", child)
			}
		}
		walk("", schema)
		data, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s schema: %d bytes", name, len(data))
	}
}

func compileSchema(t *testing.T, schema map[string]any) CompiledSchema {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	c, err := JSONSchemaValidator{}.Compile(data)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return c
}

// docsExample extracts the configuration example of docs/forum.md.
func docsExample(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../docs/forum.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(data), "## Configuration overview")
	if !ok {
		t.Fatal("docs/forum.md: no Configuration overview")
	}
	_, rest, ok = strings.Cut(rest, "```json\n")
	if !ok {
		t.Fatal("docs/forum.md: no JSON example")
	}
	example, _, ok := strings.Cut(rest, "```")
	if !ok {
		t.Fatal("docs/forum.md: unterminated JSON example")
	}
	return []byte(example)
}

// Every built-in template and the documented examples match the published
// schema; a full configuration is also a valid patch.
func TestConfigSchemaAcceptsTemplatesAndExamples(t *testing.T) {
	config, patch := compileSchema(t, ConfigSchema()), compileSchema(t, PatchSchema())
	docs := map[string][]byte{
		"docs/forum.md":   docsExample(t),
		"cfgtExampleJSON": []byte(cfgtExampleJSON),
	}
	for _, tpl := range templates {
		data, ok := templateConfig(tpl.name)
		if !ok {
			t.Fatalf("template %s missing", tpl.name)
		}
		docs["template "+tpl.name] = []byte(data)
	}
	for name, data := range docs {
		if _, err := Decode(data); err != nil {
			t.Errorf("%s does not decode: %v", name, err)
		}
		if err := config.Validate(data); err != nil {
			t.Errorf("%s against the config schema: %v", name, err)
		}
		if err := patch.Validate(data); err != nil {
			t.Errorf("%s against the patch schema: %v", name, err)
		}
	}
}

// The guide's example patches match the patch schema.
func TestPatchSchemaAcceptsGuideExamples(t *testing.T) {
	patch := compileSchema(t, PatchSchema())
	guideText := guide()
	for _, example := range []string{
		`{"sources": {"question": {"inline": "Should we use Go or Python?"}}, "participants": {"chair": {"model": "<a name from forum_models>"}}}`,
		`{"sources": {"chapter": {"file": "files/chapter2.md"}}}`,
		`{"sources": {"topic": {"inline": null, "file": "files/chapter1.md"}}}`,
	} {
		if !strings.Contains(guideText, example) {
			t.Errorf("the guide no longer shows %s", example)
		}
		if err := patch.Validate([]byte(example)); err != nil {
			t.Errorf("%s: %v", example, err)
		}
	}
}

func TestConfigSchemaRefuses(t *testing.T) {
	config, patch := compileSchema(t, ConfigSchema()), compileSchema(t, PatchSchema())
	for _, tc := range []struct {
		name, doc string
		schema    CompiledSchema
	}{
		{"unknown field", `{"version":1,"bogus":1,"brief":{"purpose":"p","task":"t"},"participants":{},"layers":[],"limits":{"max_calls":1,"max_duration_seconds":1,"call_timeout_seconds":1,"max_attempts_per_turn":1,"max_parallel_calls":1}}`, config},
		{"missing limits", `{"version":1,"brief":{"purpose":"p","task":"t"},"participants":{},"layers":[]}`, config},
		{"unknown delivery", `{"layers":[{"id":"a","participants":["x"],"instructions":"i","delivery":"sometimes","max_rounds":1,"output":{"format":"text"}}]}`, patch},
		{"incomplete layer in a patch", `{"layers":[{"id":"a"}]}`, patch},
		{"unknown participant field in a patch", `{"participants":{"bob":{"agnet":"bob"}}}`, patch},
		{"null config", `null`, patch},
	} {
		if err := tc.schema.Validate([]byte(tc.doc)); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	for _, ok := range []string{
		`{"participants":{"bob":null},"seed":null,"layers":null}`,
		`{"layers":[{"id":"a","participants":["x"],"instructions":"i","delivery":"per_turn","max_rounds":2,"output":{"format":"json","share":[]}}]}`,
		`{"participants":{"bob":{"mode":null,"model":"m"}}}`,
	} {
		if err := patch.Validate([]byte(ok)); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
}
