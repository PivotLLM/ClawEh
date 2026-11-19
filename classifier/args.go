// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
)

// Checking a definition's tool arguments against the tools' own schemas.
//
// A tool is called with whatever a definition names, and most tools ignore
// an argument they do not have: a misspelt filter or sort would change
// nothing and say nothing. So when a definition is saved or validated, the
// arguments of its source, fetch and actions are checked against each tool's
// published JSON Schema (ToolRunner.Schema), and anything the tool would not
// accept is refused, naming the argument:
//
//   - an argument the tool does not declare (unless its schema allows other
//     arguments: "additionalProperties" true or a schema);
//   - an argument the tool requires that is left out;
//   - a value of the wrong type, or not one of the values the tool lists;
//   - a paging argument (source.paging offset_arg, cursor_arg) the tool does
//     not declare.
//
// Only what the schema states is refused. A tool with no schema, a part of a
// schema written with combinators (anyOf, oneOf, allOf, not, $ref), and a
// value that is a placeholder (its type is known only per item) are not
// checked. Runs do not repeat the check: a definition saved before it
// existed keeps running, and is checked the next time it is changed.

// validateArgs checks the arguments of every tool def names against the
// tool's schema, as agentID. The issues name the argument's place in the
// definition.
func validateArgs(def *Definition, agentID string, tools ToolRunner) error {
	schemas := map[string]map[string]any{}
	schemaOf := func(tool string) map[string]any {
		s, ok := schemas[tool]
		if !ok {
			var err error
			if s, err = tools.Schema(agentID, tool); err != nil {
				s = nil // not the agent's tool: validateGrants says so
			}
			schemas[tool] = s
		}
		return s
	}
	var issues []string
	if t := def.Source.Tool; t != "" {
		supplied := map[string]bool{}
		schema := schemaOf(t)
		if p := def.Source.Paging; p != nil {
			for key, arg := range map[string]string{"offset_arg": p.OffsetArg, "cursor_arg": p.CursorArg} {
				if arg == "" {
					continue
				}
				supplied[arg] = true
				issues = append(issues, checkPagingArg(schema, t, key, arg, key == "offset_arg")...)
			}
		}
		issues = append(issues, checkToolArgs(schema, t, "source.args", def.Source.Args, supplied)...)
	}
	if f := def.Fetch; f != nil && f.Tool != "" {
		issues = append(issues, checkToolArgs(schemaOf(f.Tool), f.Tool, "fetch.args", f.Args, nil)...)
	}
	for _, name := range sortedKeys(def.Actions) {
		a := def.Actions[name]
		if a.Tool != "" {
			issues = append(issues, checkToolArgs(schemaOf(a.Tool), a.Tool, "actions."+name+".args", a.Args, nil)...)
		}
	}
	if len(issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: issues}
}

// checkPagingArg checks that tool declares the paging argument arg (the
// value of source.paging.<key>), and for the offset that it takes a number.
func checkPagingArg(schema map[string]any, tool, key, arg string, number bool) []string {
	props, closed := declared(schema)
	if props == nil {
		return nil
	}
	prop, ok := props[arg]
	if !ok {
		if !closed {
			return nil
		}
		return []string{fmt.Sprintf("source.paging.%s: %s has no argument %q; %s", key, tool, arg, argumentList(props))}
	}
	if !number {
		return nil
	}
	if types := schemaTypes(prop); len(types) > 0 && !slices.Contains(types, "integer") && !slices.Contains(types, "number") {
		return []string{fmt.Sprintf("source.paging.%s: argument %q of %s takes %s, not a number, so it cannot be the number of items to skip.",
			key, arg, tool, typeWords(types))}
	}
	return nil
}

// checkToolArgs checks args (at where in the definition) against tool's
// schema. supplied names the arguments the classifier passes itself, which
// are not missing when the definition leaves them out.
func checkToolArgs(schema map[string]any, tool, where string, args map[string]any, supplied map[string]bool) []string {
	props, closed := declared(schema)
	if props == nil {
		return nil
	}
	var issues []string
	for _, name := range sortedKeys(args) {
		prop, ok := props[name]
		switch {
		case ok:
			issues = append(issues, checkValue(where+"."+name, args[name], prop)...)
		case closed:
			issues = append(issues, fmt.Sprintf("%s.%s: %s has no argument %q; %s", where, name, tool, name, argumentList(props)))
		}
	}
	for _, name := range requiredNames(schema) {
		if _, given := args[name]; !given && !supplied[name] {
			issues = append(issues, fmt.Sprintf("%s: %s requires the argument %q.", where, tool, name))
		}
	}
	return issues
}

// declared returns the arguments an object schema declares and whether it
// takes no others. props is nil when the schema says nothing that can be
// checked (no schema, not an object, no "properties").
func declared(schema map[string]any) (props map[string]any, closed bool) {
	if schema == nil || combined(schema) {
		return nil, false
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, false
	}
	switch more := schema["additionalProperties"].(type) {
	case bool:
		return props, !more
	case map[string]any:
		return props, false
	}
	return props, true
}

// combined reports whether schema is written with a combinator or a
// reference, which this check does not evaluate.
func combined(schema map[string]any) bool {
	for _, key := range []string{"anyOf", "oneOf", "allOf", "not", "$ref"} {
		if _, ok := schema[key]; ok {
			return true
		}
	}
	return false
}

// requiredNames is a schema's "required" list, in its order.
func requiredNames(schema map[string]any) []string {
	switch list := schema["required"].(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, v := range list {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// argumentList says which arguments a tool has: "its arguments are a, b."
func argumentList(props map[string]any) string {
	if len(props) == 0 {
		return "it takes no arguments."
	}
	names := sortedKeys(props)
	if len(names) == 1 {
		return "its only argument is " + names[0] + "."
	}
	return "its arguments are " + strings.Join(names, ", ") + "."
}

// schemaTypes is a schema's "type" as a list; empty when it names none.
func schemaTypes(prop any) []string {
	schema, ok := prop.(map[string]any)
	if !ok {
		return nil
	}
	switch t := schema["type"].(type) {
	case string:
		return []string{t}
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, v := range t {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// checkValue checks the value at where against its schema: its type, its
// enum, the members of an object and the elements of a list. A placeholder
// is not checked.
func checkValue(where string, value, prop any) []string {
	schema, ok := prop.(map[string]any)
	if !ok || combined(schema) {
		return nil
	}
	if s, isText := value.(string); isText {
		if _, isPlaceholder, err := parsePlaceholder(s); isPlaceholder || err != nil {
			return nil // replaced per item; checkPlaceholders reports a malformed one
		}
	}
	kind := valueKind(value)
	if types := schemaTypes(schema); len(types) > 0 && !typeAccepts(types, kind) {
		return []string{fmt.Sprintf("%s must be %s; it is %s.", where, typeWords(types), kindWords[kind])}
	}
	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 && !inEnum(enum, value) {
		return []string{fmt.Sprintf("%s must be one of %s; it is %s.", where, enumWords(enum), compactJSON(value))}
	}
	if enum, ok := schema["enum"].([]string); ok && len(enum) > 0 {
		if s, isText := value.(string); !isText || !slices.Contains(enum, s) {
			return []string{fmt.Sprintf("%s must be one of %s; it is %s.", where, strings.Join(quoteAll(enum), ", "), compactJSON(value))}
		}
	}
	var issues []string
	switch v := value.(type) {
	case map[string]any:
		props, closed := declared(schema)
		if props == nil {
			return nil
		}
		for _, name := range sortedKeys(v) {
			member, ok := props[name]
			switch {
			case ok:
				issues = append(issues, checkValue(where+"."+name, v[name], member)...)
			case closed:
				issues = append(issues, fmt.Sprintf("%s.%s: the tool has no such member; %s", where, name,
					strings.Replace(argumentList(props), "argument", "member", 1)))
			}
		}
		for _, name := range requiredNames(schema) {
			if _, given := v[name]; !given {
				issues = append(issues, fmt.Sprintf("%s: the tool requires the member %q.", where, name))
			}
		}
	case []any:
		items, ok := schema["items"]
		if !ok {
			return nil
		}
		for i, el := range v {
			issues = append(issues, checkValue(fmt.Sprintf("%s[%d]", where, i), el, items)...)
		}
	}
	return issues
}

// The kinds of a JSON value.
const (
	kindText = iota
	kindWhole
	kindFraction
	kindBool
	kindNull
	kindList
	kindObject
	kindOther
)

var kindWords = map[int]string{
	kindText: "a text", kindWhole: "a number", kindFraction: "a number with a fraction", kindBool: "true or false",
	kindNull: "null", kindList: "a list", kindObject: "an object", kindOther: "something else",
}

// valueKind is the kind of an argument value as a definition holds it.
func valueKind(v any) int {
	switch n := v.(type) {
	case string:
		return kindText
	case bool:
		return kindBool
	case nil:
		return kindNull
	case []any:
		return kindList
	case map[string]any:
		return kindObject
	case float64:
		if n == math.Trunc(n) {
			return kindWhole
		}
		return kindFraction
	case int, int64:
		return kindWhole
	case json.Number:
		if f, err := n.Float64(); err == nil && f == math.Trunc(f) {
			return kindWhole
		}
		return kindFraction
	}
	return kindOther
}

// typeAccepts reports whether a value of kind fits one of the schema types.
func typeAccepts(types []string, kind int) bool {
	for _, t := range types {
		switch t {
		case "string":
			if kind == kindText {
				return true
			}
		case "integer":
			if kind == kindWhole {
				return true
			}
		case "number":
			if kind == kindWhole || kind == kindFraction {
				return true
			}
		case "boolean":
			if kind == kindBool {
				return true
			}
		case "null":
			if kind == kindNull {
				return true
			}
		case "array":
			if kind == kindList {
				return true
			}
		case "object":
			if kind == kindObject {
				return true
			}
		default:
			return true // a type this check does not know: not refused
		}
	}
	return false
}

// typeWords names schema types in a message: "a text", "a whole number or
// null".
func typeWords(types []string) string {
	words := map[string]string{
		"string": "a text", "integer": "a whole number", "number": "a number", "boolean": "true or false",
		"null": "null", "array": "a list", "object": "an object",
	}
	out := make([]string, 0, len(types))
	for _, t := range types {
		if w, ok := words[t]; ok {
			out = append(out, w)
		} else {
			out = append(out, t)
		}
	}
	return strings.Join(out, " or ")
}

// inEnum reports whether value is one of enum's values.
func inEnum(enum []any, value any) bool {
	want := compactJSON(value)
	for _, e := range enum {
		if compactJSON(e) == want {
			return true
		}
	}
	return false
}

// enumWords lists enum's values as JSON, in order.
func enumWords(enum []any) string {
	out := make([]string, len(enum))
	for i, e := range enum {
		out[i] = compactJSON(e)
	}
	return strings.Join(out, ", ")
}

// compactJSON is v as compact JSON, numbers in their plain form.
func compactJSON(v any) string {
	b, err := json.Marshal(plainJSON(v))
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
