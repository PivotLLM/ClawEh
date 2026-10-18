// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// documentedPaths is the path of every member Validate checks, in the
// form fieldDocs uses.
func documentedPaths() []string {
	paths := make([]string, 0, len(dataFields)+len(bookFields)+len(entryFields)+len(assetFields))
	for _, f := range dataFields {
		paths = append(paths, f.name)
	}
	for _, f := range bookFields {
		paths = append(paths, "character_book."+f.name)
	}
	for _, f := range entryFields {
		paths = append(paths, "character_book.entries[]."+f.name)
	}
	for _, name := range assetFields {
		paths = append(paths, "assets[]."+name)
	}
	return paths
}

// Every validated member has a description, and every description names a
// validated member.
func TestFieldDocsMatchFields(t *testing.T) {
	paths := documentedPaths()
	for _, p := range paths {
		if strings.TrimSpace(fieldDocs[p]) == "" {
			t.Errorf("card field %s has no description in fieldDocs", p)
		}
	}
	for p := range fieldDocs {
		if !slices.Contains(paths, p) {
			t.Errorf("fieldDocs has %s, which is not a card field", p)
		}
	}
	for p := range fieldEnums {
		if !slices.Contains(paths, p) {
			t.Errorf("fieldEnums has %s, which is not a card field", p)
		}
	}
}

// The position enum is exactly what Validate accepts.
func TestPositionsMatchValidate(t *testing.T) {
	entry := func(pos string) json.RawMessage {
		return json.RawMessage(`{"name":"B","character_book":{"entries":[{"keys":[],"content":"c","enabled":true,"insertion_order":0,"position":"` + pos + `"}]}}`)
	}
	for _, p := range Positions {
		if err := Validate(entry(p), false); err != nil {
			t.Errorf("position %s: %v", p, err)
		}
	}
	if err := Validate(entry("in_chat"), false); err == nil {
		t.Error("position in_chat accepted by Validate but not in Positions")
	}
}

// Every member is in the schema at every depth with a description; the
// patch schema leaves out the dates and allows null.
func TestSchemaProperties(t *testing.T) {
	for name, schema := range map[string]map[string]any{"card": CardSchema(), "patch": PatchSchema()} {
		patch := name == "patch"
		t.Run(name, func(t *testing.T) {
			if schema["additionalProperties"] != false {
				t.Error("data object is not closed")
			}
			for _, p := range documentedPaths() {
				s := lookup(schema, p)
				if patch && (p == creationDate || p == modificationDate) {
					if s != nil {
						t.Errorf("patch schema has %s", p)
					}
					continue
				}
				if s == nil {
					t.Errorf("%s missing from the schema", p)
					continue
				}
				if s["description"] != fieldDocs[p] {
					t.Errorf("%s description = %v", p, s["description"])
				}
				topOrBook := !strings.Contains(p, "[]")
				if typ, isArray := s["type"].([]string); patch && topOrBook && (!isArray || !slices.Contains(typ, "null")) {
					t.Errorf("patch %s does not allow null: %v", p, s["type"])
				}
			}
			req, ok := schema["required"].([]string)
			if ok == patch {
				t.Errorf("required = %v", schema["required"])
			}
			if patch && len(req) != 0 {
				t.Errorf("patch required = %v", req)
			}
			if !patch && !slices.Equal(req, []string{"name"}) {
				t.Errorf("card required = %v", req)
			}
		})
	}
}

// lookup finds the schema of the member at path, or nil.
func lookup(schema map[string]any, path string) map[string]any {
	cur := schema
	for part := range strings.SplitSeq(path, ".") {
		name, isArray := strings.CutSuffix(part, "[]")
		props, ok := cur["properties"].(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = props[name].(map[string]any); !ok {
			return nil
		}
		if isArray {
			if cur, ok = cur["items"].(map[string]any); !ok {
				return nil
			}
		}
	}
	return cur
}

// The schemas use only the portable subset of JSON Schema.
func TestSchemaPortable(t *testing.T) {
	for name, schema := range map[string]map[string]any{"card": CardSchema(), "patch": PatchSchema()} {
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
	}
}

func compile(t *testing.T, schema map[string]any) *jsonschema.Schema {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err = c.AddResource("ccv3:///schema.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("ccv3:///schema.json")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return s
}

func instance(t *testing.T, s string) any {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A card using every field matches the card schema; a patch removing and
// setting members matches the patch schema; an unknown member matches
// neither.
func TestSchemaAcceptsCards(t *testing.T) {
	card, patch := compile(t, CardSchema()), compile(t, PatchSchema())
	if err := card.Validate(instance(t, fullData)); err != nil {
		t.Errorf("full card against the card schema: %v", err)
	}
	if err := patch.Validate(instance(t, `{"nickname":null,"tags":["a"],"character_book":{"name":null,"entries":[{"keys":["k"],"content":"c","enabled":true,"insertion_order":0}]}}`)); err != nil {
		t.Errorf("patch against the patch schema: %v", err)
	}
	for name, s := range map[string]*jsonschema.Schema{"card": card, "patch": patch} {
		if err := s.Validate(instance(t, `{"name":"B","naem":"x"}`)); err == nil {
			t.Errorf("%s schema accepts an unknown member", name)
		}
	}
	if err := patch.Validate(instance(t, `{"modification_date":1}`)); err == nil {
		t.Error("patch schema accepts modification_date")
	}
}
