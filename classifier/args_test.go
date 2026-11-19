// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"strings"
	"testing"
)

// mailListSchema is the argument schema of a list tool: a folder (required),
// a page size, an offset and a sort order from a fixed list.
func mailListSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"folder": map[string]any{"type": "string"},
			"top":    map[string]any{"type": "integer"},
			"skip":   map[string]any{"type": "number"},
			"order":  map[string]any{"type": "string", "enum": []any{"asc", "desc"}},
			"unread": map[string]any{"type": "boolean"},
			"select": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"window": map[string]any{
				"type":       "object",
				"properties": map[string]any{"days": map[string]any{"type": "integer"}},
				"required":   []any{"days"},
			},
			"extra":  map[string]any{"type": "object"},
			"either": map[string]any{"anyOf": []any{map[string]any{"type": "string"}}},
		},
		"required": []any{"folder"},
	}
}

// validateWith validates the inbox definition after change, with schemas
// for its tools, and returns the refusal ("" when valid).
func validateWith(t *testing.T, schemas map[string]map[string]any, change func(def map[string]any)) string {
	t.Helper()
	e := newEnv(t)
	for tool, schema := range schemas {
		e.tools.setSchema(tool, schema)
	}
	def := definition()
	if change != nil {
		change(def)
	}
	if err := e.svc.Validate(context.Background(), e.scope, mustJSON(t, def), false); err != nil {
		return err.Error()
	}
	return ""
}

func TestValidateSourceArguments(t *testing.T) {
	schemas := map[string]map[string]any{"mail_list": mailListSchema()}
	sourceArgs := func(args map[string]any) func(map[string]any) {
		return func(def map[string]any) { obj(def["source"])["args"] = args }
	}
	cases := []struct {
		name string
		args map[string]any
		want string // "" = valid
	}{
		{"valid", map[string]any{"folder": "Inbox", "top": 50, "order": "asc", "unread": true, "select": []any{"id"}, "window": map[string]any{"days": 7}}, ""},
		{
			"undeclared",
			map[string]any{"folder": "Inbox", "$orderby": "x"},
			`source.args.$orderby: mail_list has no argument "$orderby"; its arguments are either, extra, folder, order, select, skip, top, unread, window.`,
		},
		{"misspelt", map[string]any{"folder": "Inbox", "ordr": "asc"}, `mail_list has no argument "ordr"`},
		{"required missing", map[string]any{"top": 5}, `source.args: mail_list requires the argument "folder".`},
		{"text for a number", map[string]any{"folder": "Inbox", "top": "50"}, `source.args.top must be a whole number; it is a text.`},
		{"fraction for a whole number", map[string]any{"folder": "Inbox", "top": 1.5}, `source.args.top must be a whole number; it is a number with a fraction.`},
		{"number for a text", map[string]any{"folder": 7}, `source.args.folder must be a text; it is a number.`},
		{"text for true or false", map[string]any{"folder": "Inbox", "unread": "yes"}, `source.args.unread must be true or false; it is a text.`},
		{"not in the list of values", map[string]any{"folder": "Inbox", "order": "newest"}, `source.args.order must be one of "asc", "desc"; it is "newest".`},
		{"list element", map[string]any{"folder": "Inbox", "select": []any{"id", 3}}, `source.args.select[1] must be a text; it is a number.`},
		{"object member undeclared", map[string]any{"folder": "Inbox", "window": map[string]any{"days": 7, "hours": 1}}, `source.args.window.hours: the tool has no such member; its only member is days.`},
		{"object member required", map[string]any{"folder": "Inbox", "window": map[string]any{}}, `source.args.window: the tool requires the member "days".`},
		{"object without members takes anything", map[string]any{"folder": "Inbox", "extra": map[string]any{"a": 1}}, ""},
		{"combinator is not checked", map[string]any{"folder": "Inbox", "either": 5}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateWith(t, schemas, sourceArgs(tc.args))
			if tc.want == "" && got != "" {
				t.Errorf("refused: %s", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Errorf("refusal = %q, want %q", got, tc.want)
			}
		})
	}
}

// A tool that publishes no schema, and one whose schema allows other
// arguments, are not refused for an argument they do not declare.
func TestValidateArgumentsOpenSchemas(t *testing.T) {
	stray := func(def map[string]any) { obj(obj(def["source"])["args"])["anything"] = 1 }
	if got := validateWith(t, nil, stray); got != "" {
		t.Errorf("no schema: refused: %s", got)
	}
	for name, more := range map[string]any{"true": true, "a schema": map[string]any{"type": "string"}} {
		schema := mailListSchema()
		schema["additionalProperties"] = more
		if got := validateWith(t, map[string]map[string]any{"mail_list": schema}, stray); got != "" {
			t.Errorf("additionalProperties %s: refused: %s", name, got)
		}
	}
	closed := mailListSchema()
	closed["additionalProperties"] = false
	if got := validateWith(t, map[string]map[string]any{"mail_list": closed}, stray); !strings.Contains(got, `has no argument "anything"`) {
		t.Errorf("additionalProperties false: %q", got)
	}
	none := map[string]any{"type": "object", "properties": map[string]any{}}
	if got := validateWith(t, map[string]map[string]any{"mail_list": none}, nil); !strings.Contains(got, `mail_list has no argument "folder"; it takes no arguments.`) {
		t.Errorf("a tool with no arguments: %q", got)
	}
}

// Fetch and action arguments are checked the same way; a placeholder
// counts as given and its type is not checked.
func TestValidateFetchAndActionArguments(t *testing.T) {
	schemas := map[string]map[string]any{
		"mail_read": {
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "integer"}},
			"required":   []string{"id"},
		},
		"mail_move": {
			"type": "object",
			"properties": map[string]any{
				"id":          map[string]any{"type": "string"},
				"destination": map[string]any{"type": "string"},
			},
			"required": []any{"id", "destination"},
		},
	}
	if got := validateWith(t, schemas, nil); got != "" {
		t.Fatalf("placeholders refused: %s", got)
	}
	got := validateWith(t, schemas, func(def map[string]any) {
		obj(obj(def["fetch"])["args"])["format"] = "text"
		urgent := obj(obj(def["actions"])["urgent"])
		urgent["args"] = map[string]any{"id": "{{item.id}}", "folder": "Urgent"}
	})
	for _, want := range []string{
		`fetch.args.format: mail_read has no argument "format"; its only argument is id.`,
		`actions.urgent.args.folder: mail_move has no argument "folder"; its arguments are destination, id.`,
		`actions.urgent.args: mail_move requires the argument "destination".`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "actions.low") {
		t.Errorf("a valid action was refused: %s", got)
	}
}

// The paging arguments must be the tool's own; the offset is supplied by
// the classifier, so a tool that requires it does not need it in args.
func TestValidatePagingArguments(t *testing.T) {
	schema := mailListSchema()
	schema["required"] = []any{"folder", "skip"}
	schemas := map[string]map[string]any{"mail_list": schema}
	paging := func(p map[string]any) func(map[string]any) {
		return func(def map[string]any) { obj(def["source"])["paging"] = p }
	}
	if got := validateWith(t, schemas, paging(map[string]any{"mode": "offset", "offset_arg": "skip"})); got != "" {
		t.Errorf("a declared offset argument: refused: %s", got)
	}
	got := validateWith(t, schemas, paging(map[string]any{"mode": "offset", "offset_arg": "$skip"}))
	if !strings.Contains(got, `source.paging.offset_arg: mail_list has no argument "$skip"; its arguments are`) {
		t.Errorf("an undeclared offset argument: %q", got)
	}
	got = validateWith(t, schemas, paging(map[string]any{"mode": "offset", "offset_arg": "order"}))
	if !strings.Contains(got, `source.paging.offset_arg: argument "order" of mail_list takes a text, not a number`) {
		t.Errorf("an offset argument that takes a text: %q", got)
	}
	got = validateWith(t, schemas, paging(map[string]any{"mode": "cursor", "cursor_arg": "page_token", "next": "next"}))
	if !strings.Contains(got, `source.paging.cursor_arg: mail_list has no argument "page_token"`) {
		t.Errorf("an undeclared cursor argument: %q", got)
	}
}

// Create and Update refuse the same; a definition saved before the check
// keeps running with its stray argument.
func TestSaveRefusesBadArgumentsRunsDoNot(t *testing.T) {
	e := newEnv(t)
	def := definition()
	obj(obj(def["source"])["args"])["$orderby"] = "receivedDateTime asc"
	e.create(def) // no schema yet: accepted, as before the check existed
	e.tools.setSchema("mail_list", mailListSchema())
	if rec := e.run("inbox-priority"); rec.Status != RunOK || rec.Acted != 3 {
		t.Fatalf("a run of the earlier definition = %+v", rec)
	}
	err := e.svc.Update(context.Background(), e.scope, "inbox-priority", []byte(`{"activity_hours":48}`))
	if err == nil || !strings.Contains(err.Error(), `mail_list has no argument "$orderby"`) {
		t.Fatalf("Update = %v", err)
	}
	if err = e.svc.Update(context.Background(), e.scope, "inbox-priority", []byte(`{"source":{"args":{"$orderby":null,"order":"asc"}}}`)); err != nil {
		t.Fatalf("Update removing the argument: %v", err)
	}
	def["id"] = "second"
	if _, err = e.svc.Create(context.Background(), e.scope, mustJSON(t, def)); err == nil || !strings.Contains(err.Error(), `has no argument "$orderby"`) {
		t.Fatalf("Create = %v", err)
	}
}
