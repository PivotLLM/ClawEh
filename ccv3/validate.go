// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// kind is the JSON type a card member must have.
type kind int

const (
	kindString kind = iota
	kindStringArray
	kindStringObject
	kindInt
	kindIntOrString
	kindBool
	kindObject
	kindAssets
	kindBook
	kindEntries
)

// field is one member a card object may hold.
type field struct {
	name     string
	kind     kind
	required bool
}

// dataFields are the members of a card's data object (CCv3, ClawEh table
// 2.2), in the order issues are reported.
var dataFields = []field{
	{"name", kindString, true},
	{"nickname", kindString, false},
	{"description", kindString, false},
	{"personality", kindString, false},
	{"scenario", kindString, false},
	{"first_mes", kindString, false},
	{"mes_example", kindString, false},
	{"system_prompt", kindString, false},
	{"post_history_instructions", kindString, false},
	{"alternate_greetings", kindStringArray, false},
	{"group_only_greetings", kindStringArray, false},
	{"tags", kindStringArray, false},
	{"creator", kindString, false},
	{"character_version", kindString, false},
	{"source", kindStringArray, false},
	{"creator_notes", kindString, false},
	{"creator_notes_multilingual", kindStringObject, false},
	{"creation_date", kindInt, false},
	{"modification_date", kindInt, false},
	{"assets", kindAssets, false},
	{"character_book", kindBook, false},
	{"extensions", kindObject, false},
}

// bookFields are the members of character_book (2.3).
var bookFields = []field{
	{"name", kindString, false},
	{"description", kindString, false},
	{"scan_depth", kindInt, false},
	{"token_budget", kindInt, false},
	{"recursive_scanning", kindBool, false},
	{"extensions", kindObject, false},
	{"entries", kindEntries, false},
}

// entryFields are the members of a lorebook entry (2.3).
var entryFields = []field{
	{"keys", kindStringArray, true},
	{"content", kindString, true},
	{"enabled", kindBool, true},
	{"insertion_order", kindInt, true},
	{"case_sensitive", kindBool, false},
	{"name", kindString, false},
	{"priority", kindInt, false},
	{"id", kindIntOrString, false},
	{"comment", kindString, false},
	{"selective", kindBool, false},
	{"secondary_keys", kindStringArray, false},
	{"constant", kindBool, false},
	{"position", kindString, false},
	{"extensions", kindObject, false},
	{"use_regex", kindBool, false},
}

// assetFields are the string members of an assets element.
var assetFields = []string{"type", "uri", "name", "ext"}

// The arrays the spec requires, written as [] when a card leaves them out.
var defaultArrays = []string{"alternate_greetings", "group_only_greetings", "tags"}

// The two dates ClawEh sets.
const (
	creationDate     = "creation_date"
	modificationDate = "modification_date"
)

// checker collects the issues and unknown members of one card.
type checker struct {
	issues  []Issue
	unknown []string
}

func (c *checker) add(path, msg string) {
	c.issues = append(c.issues, Issue{Path: path, Message: msg})
}

// Validate checks a card's data object against the CCv3 field rules (types,
// a non-blank name, the lorebook entry members and the position values). A
// member the spec does not define is a problem unless allowUnknown is true,
// which is how an import keeps what other apps wrote. Every problem is
// reported together in a *ValidationError.
func Validate(data json.RawMessage, allowUnknown bool) error {
	c := check(data)
	if !allowUnknown {
		for _, p := range c.unknown {
			c.add(p, "is not a card field")
		}
	}
	if len(c.issues) > 0 {
		return &ValidationError{Issues: c.issues}
	}
	return nil
}

// check validates data and returns its issues (unknown members excluded) and
// the paths of its unknown members.
func check(data json.RawMessage) *checker {
	c := &checker{}
	members, ok := object(data)
	if !ok {
		c.add("card", "must be an object")
		return c
	}
	c.object("", members, dataFields)
	if raw, ok := members["name"]; ok && isString(raw) {
		var name string
		if json.Unmarshal(raw, &name) == nil && isBlank(name) {
			c.add("name", "must not be blank")
		}
	}
	return c
}

// object checks the members of one object against its fields.
func (c *checker) object(prefix string, members map[string]json.RawMessage, fields []field) {
	for _, f := range fields {
		path := prefix + f.name
		raw, ok := members[f.name]
		if !ok {
			if f.required {
				c.add(path, "is required")
			}
			continue
		}
		c.value(path, raw, f.kind)
	}
	var unknown []string
	for k := range members {
		if !slices.ContainsFunc(fields, func(f field) bool { return f.name == k }) {
			unknown = append(unknown, prefix+k)
		}
	}
	slices.Sort(unknown)
	c.unknown = append(c.unknown, unknown...)
}

// value checks one member's value.
func (c *checker) value(path string, raw json.RawMessage, k kind) {
	switch k {
	case kindString:
		if !isString(raw) {
			c.add(path, "must be text")
		}
	case kindStringArray:
		if !isStringArray(raw) {
			c.add(path, "must be a list of text")
		}
	case kindStringObject:
		m, ok := object(raw)
		if !ok || slices.ContainsFunc(slices.Collect(maps.Values(m)), func(v json.RawMessage) bool { return !isString(v) }) {
			c.add(path, "must be an object of text values")
		}
	case kindInt:
		if !isInt(raw) {
			c.add(path, "must be a whole number")
		}
	case kindIntOrString:
		if !isInt(raw) && !isString(raw) {
			c.add(path, "must be a whole number or text")
		}
	case kindBool:
		var b bool
		if isNull(raw) || json.Unmarshal(raw, &b) != nil {
			c.add(path, "must be true or false")
		}
	case kindObject:
		if _, ok := object(raw); !ok {
			c.add(path, "must be an object")
		}
	case kindAssets:
		c.assets(path, raw)
	case kindBook:
		m, ok := object(raw)
		if !ok {
			c.add(path, "must be an object")
			return
		}
		c.object(path+".", m, bookFields)
	case kindEntries:
		c.entries(path, raw)
	}
}

func (c *checker) assets(path string, raw json.RawMessage) {
	elems, ok := array(raw)
	if !ok {
		c.add(path, "must be a list")
		return
	}
	for i, e := range elems {
		p := fmt.Sprintf("%s[%d]", path, i)
		m, ok := object(e)
		if !ok {
			c.add(p, "must be an object")
			continue
		}
		for _, name := range assetFields {
			if v, ok := m[name]; ok && !isString(v) {
				c.add(p+"."+name, "must be text")
			}
		}
	}
}

func (c *checker) entries(path string, raw json.RawMessage) {
	elems, ok := array(raw)
	if !ok {
		c.add(path, "must be a list")
		return
	}
	for i, e := range elems {
		p := fmt.Sprintf("%s[%d]", path, i)
		m, ok := object(e)
		if !ok {
			c.add(p, "must be an object")
			continue
		}
		c.object(p+".", m, entryFields)
		if v, ok := m["position"]; ok && isString(v) {
			var pos string
			if json.Unmarshal(v, &pos) == nil && pos != "before_char" && pos != "after_char" {
				c.add(p+".position", "must be before_char or after_char")
			}
		}
	}
}

// object decodes raw when it is a JSON object.
func object(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	return m, true
}

// array decodes raw when it is a JSON array.
func array(raw json.RawMessage) ([]json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false
	}
	var a []json.RawMessage
	if json.Unmarshal(raw, &a) != nil {
		return nil, false
	}
	return a, true
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

func isString(raw json.RawMessage) bool {
	var s string
	return !isNull(raw) && json.Unmarshal(raw, &s) == nil
}

func isStringArray(raw json.RawMessage) bool {
	elems, ok := array(raw)
	return ok && !slices.ContainsFunc(elems, func(e json.RawMessage) bool { return !isString(e) })
}

// isInt reports whether raw is an integer literal that fits in an int64.
func isInt(raw json.RawMessage) bool {
	_, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	return err == nil
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }
