// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"reflect"
	"strings"
	"sync"
)

// The definition's JSON Schema, published (as the tools' RawSchema) for
// classifier_create's and classifier_validate's `definition` and
// classifier_update's `changes`, so a model sees every field. It is generated
// from the Go types by reflection (field names, types, closed objects) plus
// fieldDocs (descriptions); enums and minimums are the values ValidateStatic
// checks. A test fails when fieldDocs and the types disagree. The fields
// ClawEh writes (owner, created_at, updated_at) are left out.
//
// It keeps to the subset of JSON Schema every function-calling provider
// accepts: no $ref/$defs, no oneOf/anyOf, no pattern or format, and enums on
// strings only.

// fieldDoc documents one definition field, keyed "<GoType>.<json name>".
type fieldDoc struct {
	desc string
	enum []string
	min  int // integer minimum; 0 means none
}

// hostFields are the fields ClawEh writes; the schemas leave them out.
var hostFields = map[string]bool{
	"Definition.owner":      true,
	"Definition.created_at": true,
	"Definition.updated_at": true,
}

var fieldDocs = map[string]fieldDoc{
	"Definition.id":             {desc: "Classifier ID: letters, digits, - and _, starting with a letter or digit, at most 64 characters; names the file classifiers/<id>.json"},
	"Definition.enabled":        {desc: "false keeps the classifier without running it"},
	"Definition.dry_run":        {desc: "true classifies and records decisions without running actions, kept apart from the live state"},
	"Definition.trigger":        {desc: "When the classifier runs"},
	"Definition.source":         {desc: "The tool that lists the items"},
	"Definition.fetch":          {desc: "Optional tool that reads one item; omit when the list holds everything the model needs"},
	"Definition.classify":       {desc: "The question asked about each item"},
	"Definition.actions":        {desc: "The tool run for each category, by category name; a category without one is only recorded"},
	"Definition.limits":         {desc: "Limits of one run"},
	"Definition.report":         {desc: "When a run's summary is posted to your chat, and whether failed runs are reported"},
	"Definition.activity_hours": {desc: "Hours the activity log is kept; 72 when omitted", min: 1},

	"Trigger.every_seconds": {desc: "Run every this many seconds", min: 1},

	"Source.tool":  {desc: "Tool that lists the items; one of your tools"},
	"Source.args":  {desc: "The tool's arguments; no placeholders"},
	"Source.items": {desc: "Dot-path to the list in the tool's result; \"\" when the result is the list"},
	"Source.id":    {desc: "Dot-path, inside one list element, to the item's stable ID"},

	"Fetch.tool":      {desc: "Tool that reads one item; one of your tools"},
	"Fetch.args":      {desc: "The tool's arguments; a value may be exactly {{item.<path>}}"},
	"Fetch.fields":    {desc: "What the model sees: field name (letters, digits, - and _) to a dot-path in the result; everything else is dropped"},
	"Fetch.html":      {desc: "Names of fields whose HTML is reduced to text"},
	"Fetch.max_chars": {desc: "Characters the fields may take together, the longest cut first; the install's maximum when omitted", min: 1},

	"Classify.instructions":      {desc: "What to decide about each item; may be empty when instructions_file or instructions_dir is set"},
	"Classify.instructions_file": {desc: "Path of a .md file under files/ (such as files/classifiers/inbox/guide.md) whose content follows instructions; read on every run, so edits apply on the next run"},
	"Classify.instructions_dir":  {desc: "Path of a folder under files/ (such as files/classifiers/inbox/rules); every .md file directly in it is added under its name, read on every run. Files load in alphabetical order of their names; to control the order, you can start names with zero-padded numbers such as 00-, 01-"},
	"Classify.rules":             {desc: "Rules applied in order, such as \"Email from bob@example.com is always urgent.\"; prefer one .md file per rule in instructions_dir"},
	"Classify.categories":        {desc: "The possible answers; at least one"},

	"Category.name":        {desc: "Category name: letters, digits, - and _; not \"unclassified\""},
	"Category.description": {desc: "What belongs in the category"},

	"Action.tool": {desc: "Tool run for items of the category; one of your tools"},
	"Action.args": {desc: "The tool's arguments; a value may be exactly {{item.<path>}}, {{fetched.<name>}} or {{category}}"},

	"Limits.items_per_run":       {desc: "Items handled per run at most; the rest are taken next run; the install's maximum when omitted", min: 1},
	"Limits.parallel":            {desc: "Items handled at the same time; 1 when omitted", min: 1},
	"Limits.stop_after_failures": {desc: "Consecutive failed items that stop a run; 5 when omitted", min: 1},

	"Report.summary": {desc: "on_problems (default): only when items failed or were unclassified, or the run failed; always: after every run that handled items or failed; never", enum: summaryValues},
	"Report.errors":  {desc: "true (default): a failed run is reported to you and your chat, the second in a row muting further reports for 24 hours; false: failed runs are only logged. Setting it, or any other change, lifts the mute"},
}

var (
	definitionSchema = sync.OnceValue(func() map[string]any { return structSchema(reflect.TypeFor[Definition](), false) })
	patchSchema      = sync.OnceValue(func() map[string]any { return structSchema(reflect.TypeFor[Definition](), true) })
)

// DefinitionSchema is the JSON Schema of a definition: every field a caller
// sets, with its type, description, enum and minimum, objects closed,
// nothing required (ValidateStatic says what is missing). The map is
// shared: do not modify it.
func DefinitionSchema() map[string]any { return definitionSchema() }

// PatchSchema is the JSON Schema of a merge patch of a definition:
// DefinitionSchema's structure with every member also allowed to be null.
// Arrays are replaced whole, so their items are DefinitionSchema's. The map
// is shared: do not modify it.
func PatchSchema() map[string]any { return patchSchema() }

// structSchema is the closed object schema of struct type t.
func structSchema(t reflect.Type, patch bool) map[string]any {
	props := map[string]any{}
	for f := range t.Fields() {
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		key := t.Name() + "." + name
		if name == "-" || hostFields[key] {
			continue
		}
		s := typeSchema(f.Type, key, patch)
		doc := fieldDocs[key]
		if doc.desc != "" {
			s["description"] = doc.desc
		}
		if len(doc.enum) > 0 {
			enum := make([]any, len(doc.enum))
			for i, v := range doc.enum {
				enum[i] = v
			}
			s["enum"] = enum
		}
		if doc.min != 0 {
			s["minimum"] = doc.min
		}
		if patch {
			allowNull(s)
		}
		props[name] = s
	}
	return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
}

// typeSchema is the schema of a value of type t, the type of the field key.
func typeSchema(t reflect.Type, key string, patch bool) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return structSchema(t, patch)
	case reflect.Map:
		if t.Elem().Kind() == reflect.Interface {
			return map[string]any{"type": "object", "additionalProperties": true} // tool arguments
		}
		value := typeSchema(t.Elem(), key, patch)
		if patch {
			allowNull(value)
		}
		return map[string]any{"type": "object", "additionalProperties": value}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": typeSchema(t.Elem(), key, false)}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int:
		return map[string]any{"type": "integer"}
	default:
		panic("classifier: no JSON Schema for " + t.String() + " (" + key + ")")
	}
}

// allowNull lets a patch member be null: "null" is added to its type and
// enum.
func allowNull(s map[string]any) {
	if typ, ok := s["type"].(string); ok {
		s["type"] = []string{typ, "null"}
	}
	if enum, ok := s["enum"].([]any); ok {
		s["enum"] = append(enum, nil)
	}
}
