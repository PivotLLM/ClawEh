// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// issues returns the issues of err as "path message" strings, or nil.
func issues(t *testing.T, err error) []string {
	t.Helper()
	if err == nil {
		return nil
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error %v is not a *ValidationError", err)
	}
	if !ve.Refusal() {
		t.Error("ValidationError is not a refusal")
	}
	out := make([]string, len(ve.Issues))
	for i, is := range ve.Issues {
		out[i] = is.Path + " " + is.Message
	}
	return out
}

// fullData is a card using every field of table 2.2 and 2.3.
const fullData = `{
  "name": "Sherlock Holmes",
  "nickname": "Holmes",
  "description": "A consulting detective.",
  "personality": "Cold, precise.",
  "scenario": "Baker Street.",
  "first_mes": "Ah, {{user}}.",
  "mes_example": "<START>\n{{char}}: You have been in Afghanistan, I perceive.",
  "system_prompt": "Stay in character.",
  "post_history_instructions": "Be brief.",
  "alternate_greetings": ["Come in."],
  "group_only_greetings": [],
  "tags": ["detective"],
  "creator": "Alice",
  "character_version": "1.0",
  "source": ["https://example.com"],
  "creator_notes": "Notes.",
  "creator_notes_multilingual": {"fr": "Notes."},
  "creation_date": 1700000000,
  "modification_date": 1700000001,
  "assets": [{"type": "icon", "uri": "ccdefault:", "name": "main", "ext": "png"}],
  "character_book": {
    "name": "Book",
    "description": "Lore",
    "scan_depth": 4,
    "token_budget": 500,
    "recursive_scanning": false,
    "extensions": {"x": 1},
    "entries": [{
      "keys": ["violin"],
      "content": "He plays the violin.",
      "enabled": true,
      "insertion_order": 1,
      "case_sensitive": false,
      "name": "violin",
      "priority": 1,
      "id": 7,
      "comment": "c",
      "selective": false,
      "secondary_keys": [],
      "constant": true,
      "position": "before_char",
      "extensions": {},
      "use_regex": false
    }]
  },
  "extensions": {"depth_prompt": {"depth": 4}}
}`

func TestValidateAcceptsEveryField(t *testing.T) {
	for _, allow := range []bool{false, true} {
		if err := Validate(json.RawMessage(fullData), allow); err != nil {
			t.Errorf("Validate(full, %v) = %v", allow, err)
		}
	}
	if err := Validate(json.RawMessage(`{"name":"Bob"}`), false); err != nil {
		t.Errorf("Validate(minimal) = %v", err)
	}
}

// Every refusal of table 2.2 and 2.3, one member at a time.
func TestValidateRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want []string
	}{
		{"not an object", `[]`, []string{"card must be an object"}},
		{"invalid JSON", `{`, []string{"card must be an object"}},
		{"name missing", `{}`, []string{"name is required"}},
		{"name blank", `{"name":"  "}`, []string{"name must not be blank"}},
		{"name not text", `{"name":3}`, []string{"name must be text"}},
		{"name null", `{"name":null}`, []string{"name must be text"}},
		{"nickname", `{"name":"B","nickname":1}`, []string{"nickname must be text"}},
		{"description", `{"name":"B","description":true}`, []string{"description must be text"}},
		{"personality", `{"name":"B","personality":[]}`, []string{"personality must be text"}},
		{"scenario", `{"name":"B","scenario":{}}`, []string{"scenario must be text"}},
		{"first_mes", `{"name":"B","first_mes":1}`, []string{"first_mes must be text"}},
		{"mes_example", `{"name":"B","mes_example":1}`, []string{"mes_example must be text"}},
		{"system_prompt", `{"name":"B","system_prompt":1}`, []string{"system_prompt must be text"}},
		{"post_history", `{"name":"B","post_history_instructions":1}`, []string{"post_history_instructions must be text"}},
		{"alternate_greetings", `{"name":"B","alternate_greetings":"hi"}`, []string{"alternate_greetings must be a list of text"}},
		{"group_only_greetings", `{"name":"B","group_only_greetings":[1]}`, []string{"group_only_greetings must be a list of text"}},
		{"tags", `{"name":"B","tags":[null]}`, []string{"tags must be a list of text"}},
		{"creator", `{"name":"B","creator":1}`, []string{"creator must be text"}},
		{"character_version", `{"name":"B","character_version":1}`, []string{"character_version must be text"}},
		{"source", `{"name":"B","source":1}`, []string{"source must be a list of text"}},
		{"source text", `{"name":"B","source":"https://example.com"}`, []string{"source must be a list of text"}},
		{"creator_notes", `{"name":"B","creator_notes":1}`, []string{"creator_notes must be text"}},
		{"creator_notes_multilingual", `{"name":"B","creator_notes_multilingual":{"fr":1}}`, []string{"creator_notes_multilingual must be an object of text values"}},
		{"creation_date fraction", `{"name":"B","creation_date":1.5}`, []string{"creation_date must be a whole number"}},
		{"modification_date text", `{"name":"B","modification_date":"1"}`, []string{"modification_date must be a whole number"}},
		{"assets not list", `{"name":"B","assets":{}}`, []string{"assets must be a list"}},
		{"asset not object", `{"name":"B","assets":[1]}`, []string{"assets[0] must be an object"}},
		{"asset member", `{"name":"B","assets":[{"type":1,"uri":"u"}]}`, []string{"assets[0].type must be text"}},
		{"extensions", `{"name":"B","extensions":[]}`, []string{"extensions must be an object"}},
		{"book not object", `{"name":"B","character_book":[]}`, []string{"character_book must be an object"}},
		{"book name", `{"name":"B","character_book":{"name":1}}`, []string{"character_book.name must be text"}},
		{"book description", `{"name":"B","character_book":{"description":1}}`, []string{"character_book.description must be text"}},
		{"book scan_depth", `{"name":"B","character_book":{"scan_depth":"4"}}`, []string{"character_book.scan_depth must be a whole number"}},
		{"book token_budget", `{"name":"B","character_book":{"token_budget":1.2}}`, []string{"character_book.token_budget must be a whole number"}},
		{"book recursive_scanning", `{"name":"B","character_book":{"recursive_scanning":"no"}}`, []string{"character_book.recursive_scanning must be true or false"}},
		{"book extensions", `{"name":"B","character_book":{"extensions":1}}`, []string{"character_book.extensions must be an object"}},
		{"book entries", `{"name":"B","character_book":{"entries":{}}}`, []string{"character_book.entries must be a list"}},
		{"entry not object", `{"name":"B","character_book":{"entries":[1]}}`, []string{"character_book.entries[0] must be an object"}},
		{"entry required", `{"name":"B","character_book":{"entries":[{}]}}`, []string{
			"character_book.entries[0].keys is required",
			"character_book.entries[0].content is required",
			"character_book.entries[0].enabled is required",
			"character_book.entries[0].insertion_order is required",
		}},
		{"entry types", `{"name":"B","character_book":{"entries":[{"keys":"k","content":1,"enabled":null,"insertion_order":"1",
			"case_sensitive":1,"name":1,"priority":"1","id":true,"comment":1,"selective":1,"secondary_keys":[1],"constant":"yes",
			"position":1,"extensions":[],"use_regex":0}]}}`, []string{
			"character_book.entries[0].keys must be a list of text",
			"character_book.entries[0].content must be text",
			"character_book.entries[0].enabled must be true or false",
			"character_book.entries[0].insertion_order must be a whole number",
			"character_book.entries[0].case_sensitive must be true or false",
			"character_book.entries[0].name must be text",
			"character_book.entries[0].priority must be a whole number",
			"character_book.entries[0].id must be a whole number or text",
			"character_book.entries[0].comment must be text",
			"character_book.entries[0].selective must be true or false",
			"character_book.entries[0].secondary_keys must be a list of text",
			"character_book.entries[0].constant must be true or false",
			"character_book.entries[0].position must be text",
			"character_book.entries[0].extensions must be an object",
			"character_book.entries[0].use_regex must be true or false",
		}},
		{
			"entry position", `{"name":"B","character_book":{"entries":[{"keys":[],"content":"","enabled":true,"insertion_order":0,"position":"top"}]}}`,
			[]string{"character_book.entries[0].position must be before_char or after_char"},
		},
		{"several at once", `{"name":"","tags":"x"}`, []string{"tags must be a list of text", "name must not be blank"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := issues(t, Validate(json.RawMessage(tc.data), false))
			if !slices.Equal(got, tc.want) {
				t.Errorf("issues = %q, want %q", got, tc.want)
			}
		})
	}
}

// Unknown members are refused when allowUnknown is false (create, update)
// and accepted when it is true (import), at every level the spec defines;
// inside extensions anything goes.
func TestValidateUnknownMembers(t *testing.T) {
	data := json.RawMessage(`{"name":"B","mood":"x","extensions":{"anything":1},
		"character_book":{"lore":1,"entries":[{"keys":[],"content":"","enabled":true,"insertion_order":0,"weight":2}]}}`)
	got := issues(t, Validate(data, false))
	want := []string{
		"character_book.entries[0].weight is not a card field",
		"character_book.lore is not a card field",
		"mood is not a card field",
	}
	if !slices.Equal(got, want) {
		t.Errorf("refused = %q, want %q", got, want)
	}
	if err := Validate(data, true); err != nil {
		t.Errorf("Validate(allowUnknown) = %v", err)
	}
}

func TestValidationErrorMessage(t *testing.T) {
	err := Validate(json.RawMessage(`{"tags":1}`), false)
	want := "The card is not valid: name is required; tags must be a list of text."
	if err == nil || err.Error() != want {
		t.Errorf("Error() = %v, want %q", err, want)
	}
}

// A lorebook entry id may be a whole number or text, as the spec allows.
func TestValidateEntryIDNumberOrText(t *testing.T) {
	for _, id := range []string{`7`, `"entry-7"`} {
		data := `{"name":"B","character_book":{"entries":[{"keys":[],"content":"","enabled":true,"insertion_order":0,"id":` + id + `}]}}`
		if err := Validate(json.RawMessage(data), false); err != nil {
			t.Errorf("id %s: %v", id, err)
		}
	}
}
