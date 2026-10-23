// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"errors"
	"strings"
	"testing"
)

func TestCompileSchema(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		wantErr string // "" compiles
	}{
		{"example findings", `{"type":"object","required":["agreements"],"properties":{"agreements":{"type":"array","items":{"type":"string"}}}}`, ""},
		{"empty schema", `{}`, ""},
		{"boolean schema", `true`, ""},
		{"internal ref", `{"$defs":{"s":{"type":"string"}},"$ref":"#/$defs/s"}`, ""},
		{"internal ref via own id", `{"$id":"https://example.com/root","$defs":{"s":{"type":"string"}},"items":{"$ref":"#/$defs/s"}}`, ""},
		{"explicit 2020-12 dialect", `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string"}`, ""},
		{"external absolute ref", `{"$ref":"https://example.com/other.json"}`, "only references inside the schema"},
		{"external relative ref", `{"$ref":"other.json"}`, "only references inside the schema"},
		{"external file ref", `{"properties":{"a":{"$ref":"file:///etc/passwd"}}}`, "only references inside the schema"},
		{"dangling internal ref", `{"$ref":"#/$defs/missing"}`, "compile schema"},
		{"invalid keyword value", `{"type":5}`, "compile schema"},
		{"invalid minLength", `{"minLength":-1}`, "compile schema"},
		{"malformed JSON", `{"type":`, "compile schema"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sch, err := compileSchema(cfgtRaw(tt.schema))
			if tt.wantErr == "" {
				if err != nil || sch == nil {
					t.Fatalf("Compile: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Compile err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestCompiledSchemaValidate(t *testing.T) {
	sch, err := compileSchema(cfgtRaw(`{
		"type":"object","required":["agreements","disagreements"],"additionalProperties":false,
		"properties":{
			"agreements":{"type":"array","items":{"type":"string"}},
			"disagreements":{"type":"array","items":{"type":"string"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		instance string
		// wantMsgs are substrings each expected in some violation message;
		// nil means valid.
		wantMsgs []string
		plainErr bool
	}{
		{"valid", `{"agreements":["a"],"disagreements":[]}`, nil, false},
		{"missing member", `{"agreements":[]}`, []string{"/: ", "disagreements"}, false},
		{"wrong item type", `{"agreements":[1],"disagreements":[]}`, []string{"/agreements/0: "}, false},
		{"extra member", `{"agreements":[],"disagreements":[],"x":1}`, []string{"x"}, false},
		{"several failures", `{"agreements":[1, true]}`, []string{"/agreements/0", "/agreements/1", "disagreements"}, false},
		{"not JSON", `{"agreements":`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sch.Validate([]byte(tt.instance))
			switch {
			case tt.plainErr:
				if err == nil {
					t.Fatal("want an error for malformed JSON")
				}
				if isViolation := errors.As(err, new(*SchemaViolationError)); isViolation {
					t.Fatalf("malformed JSON must not be a schema violation: %v", err)
				}
			case tt.wantMsgs == nil:
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
			default:
				ve, ok := errors.AsType[*SchemaViolationError](err)
				if !ok {
					t.Fatalf("want *SchemaViolationError, got %v", err)
				}
				joined := strings.Join(ve.Messages, "\n")
				for _, want := range tt.wantMsgs {
					if !strings.Contains(joined, want) {
						t.Errorf("messages %q lack %q", ve.Messages, want)
					}
				}
			}
		})
	}
}

func TestSchemaViolationErrorText(t *testing.T) {
	if got := (&SchemaViolationError{}).Error(); got != "schema violation" {
		t.Errorf("empty = %q", got)
	}
	if got := (&SchemaViolationError{Messages: []string{"/a: x", "/b: y"}}).Error(); got != "schema violation: /a: x; /b: y" {
		t.Errorf("joined = %q", got)
	}
}
