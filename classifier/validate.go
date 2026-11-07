// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/PivotLLM/ClawEh/internal/dotpath"
)

// decodeDefinition parses a definition strictly: it must be one JSON object
// and every key must be known.
func decodeDefinition(raw []byte) (*Definition, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, &ValidationError{Issues: []string{"The definition must be a JSON object."}}
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var def Definition
	if err := dec.Decode(&def); err != nil {
		return nil, &ValidationError{Issues: []string{"The definition is not valid: " + strings.TrimPrefix(err.Error(), "json: ") + "."}}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, &ValidationError{Issues: []string{"The definition must be one JSON object, with nothing after it."}}
	}
	return &def, nil
}

// ToolPrefix starts the name of every classifier tool as the host mounts
// them (classifier_list, classifier_update, ...).
const ToolPrefix = "classifier_"

// IsClassifierTool reports whether tool is one of the classifier tools,
// which a classifier may not call: not as its source, fetch or any action.
func IsClassifierTool(tool string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(tool)), ToolPrefix)
}

// ClassifierToolRefusal is the sentence refusing a classifier tool where a
// classifier names one. field is where the definition names it (source,
// fetch, actions.<category>); "" when it is not known.
func ClassifierToolRefusal(tool, field string) string {
	if field == "" {
		return tool + " can't be used by a classifier."
	}
	return tool + " can't be used by a classifier (" + field + ")."
}

// idRuleText is the id rule for messages.
const idRuleText = "letters, digits, - and _, starting with a letter or digit, at most 64 characters"

// ValidateStatic checks a definition on its own and against the install's
// maximums c (a zero maximum is no maximum). It returns a *ValidationError
// with one sentence per issue, naming the key (and the maximum), or nil.
func ValidateStatic(def *Definition, c Ceilings) error {
	var issues []string
	add := func(format string, a ...any) { issues = append(issues, fmt.Sprintf(format, a...)) }
	over := func(key string, got, limit int, unit string) {
		if limit > 0 && got > limit {
			add("%s is %d%s; this install allows %d.", key, got, unit, limit)
		}
	}
	nonNegative := func(key string, v int) bool {
		if v < 0 {
			add("%s must be 0 (the default) or more.", key)
			return false
		}
		return true
	}

	switch {
	case strings.TrimSpace(def.ID) == "":
		add("id is required.")
	case !ValidID(def.ID):
		add("id %q must use only %s.", def.ID, idRuleText)
	}
	if def.Trigger.EverySeconds <= 0 {
		add("trigger.every_seconds must be 1 or more.")
	}

	// Source.
	if strings.TrimSpace(def.Source.Tool) == "" {
		add("source.tool is required.")
	} else if IsClassifierTool(def.Source.Tool) {
		add("%s", ClassifierToolRefusal(def.Source.Tool, "source"))
	}
	if strings.TrimSpace(def.Source.ID) == "" {
		add("source.id is required.")
	} else if !validPath(def.Source.ID) {
		add("source.id %q is not a dot-path.", def.Source.ID)
	}
	if def.Source.Items != "" && !validPath(def.Source.Items) {
		add("source.items %q is not a dot-path.", def.Source.Items)
	}
	issues = append(issues, checkPlaceholders(def.Source.Args, "source.args", nil, nil)...)

	// Fetch.
	var fields map[string]string
	if f := def.Fetch; f != nil {
		fields = f.Fields
		if strings.TrimSpace(f.Tool) == "" {
			add("fetch.tool is required.")
		} else if IsClassifierTool(f.Tool) {
			add("%s", ClassifierToolRefusal(f.Tool, "fetch"))
		}
		if len(f.Fields) == 0 {
			add("fetch.fields needs at least one field.")
		}
		for _, name := range sortedKeys(f.Fields) {
			if !ValidID(name) {
				add("fetch.fields name %q must use only %s.", name, idRuleText)
			}
			if !validPath(f.Fields[name]) {
				add("fetch.fields.%s: %q is not a dot-path.", name, f.Fields[name])
			}
		}
		seen := map[string]bool{}
		for _, name := range f.HTML {
			if _, ok := f.Fields[name]; !ok {
				add("fetch.html names %q, which is not a key of fetch.fields.", name)
			}
			if seen[name] {
				add("fetch.html names %q twice.", name)
			}
			seen[name] = true
		}
		if nonNegative("fetch.max_chars", f.MaxChars) {
			over("fetch.max_chars", f.MaxChars, c.MaxItemChars, " characters")
		}
		issues = append(issues, checkPlaceholders(f.Args, "fetch.args", []string{placeholderItem}, nil)...)
	}

	// Classify.
	if strings.TrimSpace(def.Classify.Instructions) == "" {
		add("classify.instructions is required.")
	}
	if c.MaxRules > 0 && len(def.Classify.Rules) > c.MaxRules {
		add("classify.rules has %d rules; this install allows %d.", len(def.Classify.Rules), c.MaxRules)
	}
	promptChars := utf8.RuneCountInString(def.Classify.Instructions)
	for i, r := range def.Classify.Rules {
		if strings.TrimSpace(r) == "" {
			add("classify.rules[%d] is empty.", i)
		}
		promptChars += utf8.RuneCountInString(r)
	}
	if c.MaxPromptChars > 0 && promptChars > c.MaxPromptChars {
		add("classify.instructions and classify.rules have %d characters together; this install allows %d.", promptChars, c.MaxPromptChars)
	}
	if len(def.Classify.Categories) == 0 {
		add("classify.categories needs at least one category.")
	}
	names := map[string]bool{}
	for i, cat := range def.Classify.Categories {
		switch {
		case !ValidID(cat.Name):
			add("classify.categories[%d].name %q must use only %s.", i, cat.Name, idRuleText)
		case equalFoldASCII(cat.Name, Unclassified):
			add("classify.categories[%d].name %q is reserved.", i, cat.Name)
		case names[NormalizeID(cat.Name)]:
			add("classify.categories[%d].name %q is used twice.", i, cat.Name)
		}
		names[NormalizeID(cat.Name)] = true
	}

	// Actions.
	for _, name := range sortedKeys(def.Actions) {
		a := def.Actions[name]
		if _, ok := def.categoryNamed(name); !ok || name != categoryName(def, name) {
			add("actions.%s names no category.", name)
		}
		if strings.TrimSpace(a.Tool) == "" {
			add("actions.%s.tool is required.", name)
		} else if IsClassifierTool(a.Tool) {
			add("%s", ClassifierToolRefusal(a.Tool, "actions."+name))
		}
		issues = append(issues, checkPlaceholders(a.Args, "actions."+name+".args",
			[]string{placeholderItem, placeholderFetched, placeholderCategory}, fields)...)
	}

	// Limits, retention, report.
	if nonNegative("limits.items_per_run", def.Limits.ItemsPerRun) {
		over("limits.items_per_run", def.Limits.ItemsPerRun, c.MaxItemsPerRun, "")
	}
	if nonNegative("limits.parallel", def.Limits.Parallel) {
		over("limits.parallel", def.Limits.Parallel, c.MaxParallel, "")
	}
	nonNegative("limits.stop_after_failures", def.Limits.StopAfterFailures)
	if nonNegative("activity_hours", def.ActivityHours) {
		over("activity_hours", def.ActivityHours, c.MaxActivityHours, " hours")
	}
	if def.Report.Summary != "" && !slices.Contains(summaryValues, def.Report.Summary) {
		add("report.summary must be %s.", strings.Join(quoteAll(summaryValues), ", "))
	}

	if len(issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: issues}
}

// categoryName is the exact name of the category matching name ignoring
// case, or "".
func categoryName(def *Definition, name string) string {
	c, _ := def.categoryNamed(name)
	return c.Name
}

// toolsNamed lists every tool the definition calls, in order: source, fetch,
// then the actions by category.
func toolsNamed(def *Definition) []string {
	out := []string{def.Source.Tool}
	if def.Fetch != nil {
		out = append(out, def.Fetch.Tool)
	}
	for _, name := range sortedKeys(def.Actions) {
		out = append(out, def.Actions[name].Tool)
	}
	seen := map[string]bool{}
	uniq := out[:0]
	for _, t := range out {
		if t != "" && !seen[t] {
			seen[t] = true
			uniq = append(uniq, t)
		}
	}
	return uniq
}

// validateGrants checks that agentID has every tool the definition names
// (design 6.1c); the issues name the tool.
func validateGrants(def *Definition, agentID string, tools ToolRunner) error {
	var issues []string
	for _, t := range toolsNamed(def) {
		if err := tools.Has(agentID, t); err != nil {
			issues = append(issues, err.Error()+".")
		}
	}
	if len(issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: issues}
}

// listedItem is one element of the source's list with its id.
type listedItem struct {
	index int
	id    string
	value any
}

// listItems calls the source tool as agentID and returns its elements with
// their ids, in source order. It fails when the result is not JSON, when
// source.items is not a list, or when an element's source.id resolves to
// nothing (naming the element's index).
func listItems(ctx context.Context, def *Definition, agentID string, tools ToolRunner) ([]listedItem, error) {
	ctx = withClassifier(ctx, NormalizeID(def.ID))
	result, err := tools.Call(ctx, agentID, def.Source.Tool, plainArgs(def.Source.Args))
	if err != nil {
		return nil, fmt.Errorf("source %s: %w", def.Source.Tool, err)
	}
	decoded, err := decodeJSON(result)
	if err != nil {
		return nil, fmt.Errorf("source %s did not return JSON", def.Source.Tool)
	}
	list, ok := dotpath.Get(decoded, def.Source.Items).([]any)
	if !ok {
		where := "its result"
		if def.Source.Items != "" {
			where = "source.items " + strconv.Quote(def.Source.Items)
		}
		return nil, fmt.Errorf("source %s returned no list at %s", def.Source.Tool, where)
	}
	items := make([]listedItem, 0, len(list))
	for i, el := range list {
		id, ok := itemID(dotpath.Get(el, def.Source.ID))
		if !ok {
			return nil, fmt.Errorf("source.id %q resolves to nothing in element %d of the source's list", def.Source.ID, i)
		}
		items = append(items, listedItem{index: i, id: id, value: el})
	}
	return items, nil
}

// itemID turns a selected id into its string: a non-empty string or a
// number (its exact digits).
func itemID(v any) (string, bool) {
	switch id := v.(type) {
	case string:
		if strings.TrimSpace(id) == "" {
			return "", false
		}
		return id, true
	case json.Number:
		return id.String(), true
	default:
		return "", false
	}
}

// decodeJSON decodes a tool result keeping numbers exact (json.Number).
func decodeJSON(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data")
	}
	return v, nil
}

// plainArgs is a definition's static arguments as passed to a tool (a copy,
// so a tool cannot change the definition).
func plainArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	if m, ok := plainJSON(args).(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func quoteAll(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = strconv.Quote(s)
	}
	return out
}
