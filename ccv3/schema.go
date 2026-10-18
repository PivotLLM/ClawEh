// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import "sync"

// The JSON Schemas of a card's data object (CardSchema) and of a merge patch
// over it (PatchSchema), published by the ccv3 tool for its card and changes
// arguments so a model sees every field. They are generated from the field
// tables Validate checks (dataFields, bookFields, entryFields, assetFields)
// plus fieldDocs, so they cannot drift from what the store accepts; a test
// fails when a field has no description or a description names no field.
//
// They keep to the subset of JSON Schema every function-calling provider
// accepts: no $ref/$defs, no oneOf/anyOf, no pattern or format, and enums on
// strings only.

// Positions are the values a lorebook entry's position may take.
var Positions = []string{"before_char", "after_char"}

// fieldDocs describes every card member, keyed by its path: "name",
// "character_book.scan_depth", "character_book.entries[].keys",
// "assets[].uri".
var fieldDocs = map[string]string{
	"name":                       "The character's name; required, not blank",
	"nickname":                   "What the character is called in text; replaces name for {{char}} when set",
	"description":                "Who the character is: appearance, history, situation",
	"personality":                "A short summary of the character's personality",
	"scenario":                   "The circumstances and context of the conversation",
	"first_mes":                  "The character's first message (greeting)",
	"mes_example":                "Example dialogue showing how the character speaks; separate examples with <START>",
	"system_prompt":              "Instructions for the model, placed before the character",
	"post_history_instructions":  "Instructions placed after the conversation history",
	"alternate_greetings":        "Other first messages",
	"group_only_greetings":       "First messages used only in group chats",
	"tags":                       "Labels for finding the card",
	"creator":                    "Who made the card",
	"character_version":          "The card's own version label",
	"source":                     "Where the card came from, such as a URL",
	"creator_notes":              "Notes from the creator for people using the card; not shown to the model",
	"creator_notes_multilingual": "creator_notes by language code (such as fr)",
	"creation_date":              "Set automatically (Unix seconds); leave it out",
	"modification_date":          "Set automatically (Unix seconds); leave it out",
	"assets":                     "Images and other files the card refers to; kept, not used",
	"character_book":             "The lorebook: background entries about the character and its world",
	"extensions":                 "Data other apps keep with the card; kept as is",

	"character_book.name":               "The lorebook's name",
	"character_book.description":        "What the lorebook covers",
	"character_book.scan_depth":         "How many recent messages are scanned for keys",
	"character_book.token_budget":       "Token budget for inserted entries",
	"character_book.recursive_scanning": "Whether entry content is scanned for further keys",
	"character_book.extensions":         "Data other apps keep with the lorebook; kept as is",
	"character_book.entries":            "The lorebook's entries",

	"character_book.entries[].keys":            "Words that trigger the entry; required",
	"character_book.entries[].content":         "The entry's text; required",
	"character_book.entries[].enabled":         "Whether the entry is used; required",
	"character_book.entries[].insertion_order": "Lower numbers are placed first; required",
	"character_book.entries[].case_sensitive":  "Whether keys match case",
	"character_book.entries[].name":            "The entry's name",
	"character_book.entries[].priority":        "Which entries are dropped first when over budget (lower first)",
	"character_book.entries[].id":              "The entry's number",
	"character_book.entries[].comment":         "A note about the entry; not shown to the model",
	"character_book.entries[].selective":       "Whether secondary_keys must also match",
	"character_book.entries[].secondary_keys":  "Further keys, used when selective is true",
	"character_book.entries[].constant":        "Always use the entry, whatever the keys",
	"character_book.entries[].position":        "Where the entry is placed relative to the character",
	"character_book.entries[].extensions":      "Data other apps keep with the entry; kept as is",
	"character_book.entries[].use_regex":       "Whether keys are regular expressions",

	"assets[].type": "What the asset is, such as icon or background",
	"assets[].uri":  "Where the asset is",
	"assets[].name": "The asset's name",
	"assets[].ext":  "The file type, such as png",
}

// fieldEnums gives the string members limited to a set of values.
var fieldEnums = map[string][]string{
	"character_book.entries[].position": Positions,
}

var (
	cardSchema  = sync.OnceValue(func() map[string]any { return objectSchema("", dataFields, false) })
	patchSchema = sync.OnceValue(func() map[string]any { return objectSchema("", dataFields, true) })
)

// CardSchema is the JSON Schema of a card's data object: every field with
// its type and description, closed where Validate refuses unknown members,
// name required. The map is shared: do not modify it.
func CardSchema() map[string]any { return cardSchema() }

// PatchSchema is the JSON Schema of an RFC 7386 merge patch over a card's
// data object: CardSchema's structure without the two dates (a patch may not
// set them), nothing required, and every object member also allowed to be
// null (which removes it). Arrays are replaced whole, so their items are
// CardSchema's. The map is shared: do not modify it.
func PatchSchema() map[string]any { return patchSchema() }

// objectSchema is the closed object schema of fields at path prefix
// ("" for data, "character_book." for the book, ...).
func objectSchema(prefix string, fields []field, patch bool) map[string]any {
	props := map[string]any{}
	var required []string
	for _, f := range fields {
		if patch && prefix == "" && (f.name == creationDate || f.name == modificationDate) {
			continue
		}
		path := prefix + f.name
		s := kindSchema(path, f.kind, patch)
		s["description"] = fieldDocs[path]
		if enum, ok := fieldEnums[path]; ok {
			values := make([]any, len(enum))
			for i, v := range enum {
				values[i] = v
			}
			s["enum"] = values
		}
		if patch {
			allowNull(s)
		}
		if f.required && !patch {
			required = append(required, f.name)
		}
		props[f.name] = s
	}
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// kindSchema is the schema of a member of kind k at path.
func kindSchema(path string, k kind, patch bool) map[string]any {
	switch k {
	case kindString:
		return map[string]any{"type": "string"}
	case kindStringArray:
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	case kindStringObject:
		return map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	case kindInt:
		return map[string]any{"type": "integer"}
	case kindBool:
		return map[string]any{"type": "boolean"}
	case kindObject:
		return map[string]any{"type": "object", "additionalProperties": true}
	case kindAssets:
		props := map[string]any{}
		for _, name := range assetFields {
			props[name] = map[string]any{"type": "string", "description": fieldDocs[path+"[]."+name]}
		}
		// Validate checks only these members of an asset; others are kept.
		return map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "properties": props, "additionalProperties": true,
		}}
	case kindBook:
		return objectSchema(path+".", bookFields, patch)
	case kindEntries:
		// A patch replaces an array whole: its items are the card form.
		return map[string]any{"type": "array", "items": objectSchema(path+"[].", entryFields, false)}
	default:
		panic("ccv3: no JSON Schema for member " + path)
	}
}

// allowNull adds "null" to s's type, and to its enum when it has one.
func allowNull(s map[string]any) {
	if typ, ok := s["type"].(string); ok {
		s["type"] = []string{typ, "null"}
	}
	if enum, ok := s["enum"].([]any); ok {
		s["enum"] = append(enum, nil)
	}
}
