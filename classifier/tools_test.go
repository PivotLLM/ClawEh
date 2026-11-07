// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/PivotLLM/toolspec"
)

// definitionFieldKeys lists every "<GoType>.<json name>" reachable from
// Definition that a caller sets.
func definitionFieldKeys() []string {
	seen := map[reflect.Type]bool{}
	var keys []string
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		for f := range t.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			key := t.Name() + "." + name
			if hostFields[key] {
				continue
			}
			keys = append(keys, key)
			walk(f.Type)
		}
	}
	walk(reflect.TypeFor[Definition]())
	sort.Strings(keys)
	return keys
}

// Every definition field has a description and every description names a
// field.
func TestDefinitionSchemaDocsMatchTypes(t *testing.T) {
	fields := definitionFieldKeys()
	for _, key := range fields {
		if fieldDocs[key].desc == "" {
			t.Errorf("definition field %s has no description in fieldDocs", key)
		}
	}
	for key := range fieldDocs {
		if !slices.Contains(fields, key) {
			t.Errorf("fieldDocs has %s, which is not a definition field", key)
		}
	}
}

func TestDefinitionSchemas(t *testing.T) {
	s := DefinitionSchema()
	props := obj(s["properties"])
	for _, host := range []string{"owner", "created_at", "updated_at"} {
		if _, ok := props[host]; ok {
			t.Errorf("schema publishes %s, which ClawEh writes", host)
		}
	}
	if s["additionalProperties"] != false {
		t.Error("definition schema is not closed")
	}
	summary := obj(obj(obj(props["report"])["properties"])["summary"])
	if !reflect.DeepEqual(summary["enum"], []any{"on_problems", "always", "never"}) {
		t.Errorf("report.summary enum = %v", summary["enum"])
	}
	trigger := obj(obj(props["trigger"])["properties"])
	if len(trigger) != 1 || trigger["every_seconds"] == nil {
		t.Errorf("trigger properties = %v, want every_seconds only", trigger)
	}
	patch := obj(obj(PatchSchema()["properties"])["dry_run"])
	if !reflect.DeepEqual(patch["type"], []string{"boolean", "null"}) {
		t.Errorf("patch dry_run type = %v", patch["type"])
	}
	// The schema stays in the provider-common subset.
	data, err := json.Marshal(PatchSchema())
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"$ref", "$defs", "oneOf", "anyOf", "pattern", "format"} {
		if strings.Contains(string(data), `"`+banned+`"`) {
			t.Errorf("schema uses %s", banned)
		}
	}
}

// fakeToolHost returns a fixed scope, or refuses.
type fakeToolHost struct {
	scope Scope
	err   error
}

func (h fakeToolHost) Scope(*toolspec.ToolCall) (Scope, error) { return h.scope, h.err }

func toolByName(t *testing.T, defs []toolspec.ToolDefinition, name string) toolspec.ToolDefinition {
	t.Helper()
	for _, d := range defs {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no tool %s", name)
	return toolspec.ToolDefinition{}
}

func call(t *testing.T, defs []toolspec.ToolDefinition, name string, args map[string]any) *toolspec.Result {
	t.Helper()
	res, err := toolByName(t, defs, name).Handler(&toolspec.ToolCall{Ctx: context.Background(), Args: args, AgentID: "alice"})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func TestTools(t *testing.T) {
	e := newEnv(t)
	defs := Tools(e.svc, fakeToolHost{scope: e.scope})
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
		if d.Name != "readme" && !strings.HasSuffix(d.Description, readFirst) {
			t.Errorf("%s description does not say to read the readme first", d.Name)
		}
	}
	want := []string{"readme", "list", "get", "create", "update", "delete", "validate", "status", "test", "rerun", "activity"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}

	if res := call(t, defs, "readme", nil); res.IsError || !strings.Contains(res.ForLLM, "## Substitution") ||
		!strings.Contains(res.ForLLM, "## This install's maximums") || !strings.Contains(res.ForLLM, "- `max_rules`: 100") {
		t.Errorf("readme = %s", res.ForLLM)
	}
	if res := call(t, defs, "list", map[string]any{}); res.IsError || strings.TrimSpace(res.ForLLM) != "[]" {
		t.Errorf("empty list = %s", res.ForLLM)
	}
	var def map[string]any
	if err := json.Unmarshal(mustJSON(t, definition()), &def); err != nil {
		t.Fatal(err)
	}
	if res := call(t, defs, "create", map[string]any{"definition": def}); res.IsError || res.ForLLM != "Classifier inbox-priority created." {
		t.Fatalf("create = %s", res.ForLLM)
	}
	if res := call(t, defs, "create", map[string]any{"definition": def}); !res.IsError || !strings.Contains(res.ForLLM, "already exists") {
		t.Errorf("second create = %s", res.ForLLM)
	}
	if res := call(t, defs, "get", map[string]any{"id": "inbox-priority"}); res.IsError || !strings.Contains(res.ForLLM, `"owner": "alice"`) {
		t.Errorf("get = %s", res.ForLLM)
	}
	if res := call(t, defs, "get", map[string]any{"id": "nope"}); !res.IsError || res.ForLLM != "Classifier nope does not exist." {
		t.Errorf("get unknown = %s", res.ForLLM)
	}
	if res := call(t, defs, "update", map[string]any{"id": "inbox-priority", "changes": map[string]any{"classify": map[string]any{"rules": []any{"a", "b"}}}}); res.IsError {
		t.Errorf("update = %s", res.ForLLM)
	}
	e.setMaxRules(1)
	if res := call(t, defs, "update", map[string]any{"id": "inbox-priority", "changes": map[string]any{"classify": map[string]any{"rules": []any{"a", "b"}}}}); !res.IsError ||
		res.ForLLM != "classify.rules has 2 rules; this install allows 1." {
		t.Errorf("update over max_rules = %s", res.ForLLM)
	}
	e.setMaxRules(100)
	if res := call(t, defs, "validate", map[string]any{"definition": def}); res.IsError || res.ForLLM != "The definition is valid." {
		t.Errorf("validate = %s", res.ForLLM)
	}
	if res := call(t, defs, "test", map[string]any{"id": "inbox-priority", "item_id": "m2"}); res.IsError || !strings.Contains(res.ForLLM, `"category": "low"`) {
		t.Errorf("test = %s", res.ForLLM)
	}
	if res := call(t, defs, "rerun", map[string]any{"id": "inbox-priority", "item_id": "zz"}); !res.IsError || res.ForLLM != "The source does not list that item." {
		t.Errorf("rerun unknown item = %s", res.ForLLM)
	}
	if res := call(t, defs, "status", map[string]any{"id": "inbox-priority"}); res.IsError || !strings.Contains(res.ForLLM, `"counts"`) {
		t.Errorf("status = %s", res.ForLLM)
	}
	if res := call(t, defs, "activity", map[string]any{"id": "inbox-priority", "limit": 5.0}); res.IsError || strings.TrimSpace(res.ForLLM) != "[]" {
		t.Errorf("activity = %s", res.ForLLM)
	}
	if res := call(t, defs, "activity", map[string]any{"id": "inbox-priority", "limit": -1.0}); !res.IsError {
		t.Errorf("negative limit accepted: %s", res.ForLLM)
	}
	if res := call(t, defs, "get", map[string]any{"id": "inbox-priority", "classifier": "x"}); !res.IsError || res.ForLLM != "Unknown argument classifier; use id." {
		t.Errorf("unknown argument = %s", res.ForLLM)
	}
	if res := call(t, defs, "delete", map[string]any{"id": "inbox-priority"}); res.IsError {
		t.Errorf("delete = %s", res.ForLLM)
	}

	// A scope refusal (the switch is off) is returned as the error.
	refused := Tools(e.svc, fakeToolHost{err: errors.New("classifiers are off")})
	if _, err := toolByName(t, refused, "list").Handler(&toolspec.ToolCall{Ctx: context.Background()}); err == nil {
		t.Error("a scope refusal was not returned")
	}
}
