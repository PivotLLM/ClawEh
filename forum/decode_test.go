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

func TestDecodeExample(t *testing.T) {
	cfg, err := Decode([]byte(cfgtExampleJSON))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if cfg.Version != 1 || cfg.Name != "design-review" {
		t.Errorf("version/name = %d/%q", cfg.Version, cfg.Name)
	}
	if got := len(cfg.Participants); got != 4 {
		t.Errorf("participants = %d, want 4", got)
	}
	ids := make([]string, 0, len(cfg.Layers))
	for _, l := range cfg.Layers {
		ids = append(ids, l.ID)
	}
	if strings.Join(ids, ",") != "review,debate,report" {
		t.Errorf("layer order = %v, want review,debate,report", ids)
	}
	if cfg.Layers[1].Moderator == nil || cfg.Layers[1].Moderator.Participant != "chair" {
		t.Errorf("debate moderator = %+v", cfg.Layers[1].Moderator)
	}
	if !cfg.Layers[2].Inputs[2].Optional {
		t.Error("report's debate input should be optional")
	}
	if cfg.Seed != nil {
		t.Errorf("seed = %v, want nil (absent)", *cfg.Seed)
	}
}

func TestDecodeRejects(t *testing.T) {
	minimal := func(extra string) string {
		return `{"version":1` + extra + `}`
	}
	// The last four decode as documents: the configuration comes back with
	// the issues, so ValidateStatic's findings can be reported too.
	tests := []struct {
		name string
		in   string
		path string
		want []string
	}{
		{"empty input", ``, "", []string{"malformed JSON", "line 1, column 1"}},
		{"syntax error with position", "{\n\"version\" 1}", "", []string{"malformed JSON", "line 2, column 11"}},
		{"unterminated", `{"version":1`, "", []string{"malformed JSON"}},
		{"trailing value", `{"version":1} {}`, "", []string{"trailing content", "line 1, column 15"}},
		{"trailing brace", `{"version":1}}`, "", []string{"trailing content", "column 14"}},
		{"root not object", `[]`, "", []string{"want an object", "array"}},
		{"unknown top-level field", minimal(`,"personas":{}`), "personas", []string{`unknown field "personas"`, "participants"}},
		{"field name case must match", `{"Version":1}`, "Version", []string{`unknown field "Version"`}},
		{"unknown nested field", minimal(`,"layers":[{"id":"a"},{"id":"b","moderator":{"persona":"x"}}]`), "layers[1].moderator.persona", []string{`unknown field "persona"`, "allow_directed"}},
		{"unknown participant field", minimal(`,"participants":{"alice":{"agent":"alice","tools":[]}}`), "participants.alice.tools", []string{`unknown field "tools"`}},
		{"unknown route field", minimal(`,"layers":[{"inputs":[{"from":"source:a","filter":1}]}]`), "layers[0].inputs[0].filter", []string{"unknown field"}},
		{"unknown limits field", minimal(`,"limits":{"max_output_tokens":5}`), "limits.max_output_tokens", []string{"unknown field"}},
		{"duplicate top-level key", `{"version":1,"name":"a","name":"b"}`, "name", []string{`duplicate key "name"`}},
		{"duplicate participant ID", minimal(`,"participants":{"alice":{"agent":"alice"},"alice":{"clone":"alice"}}`), "participants.alice", []string{`duplicate key "alice"`}},
		{"duplicate source ID", minimal(`,"sources":{"s":{"decode":"text","inline":"a"},"s":{"decode":"text","inline":"b"}}`), "sources.s", []string{"duplicate key"}},
		{"duplicate schema ID", minimal(`,"schemas":{"x":{},"x":{}}`), "schemas.x", []string{"duplicate key"}},
		{"duplicate key inside a schema", minimal(`,"schemas":{"x":{"type":"object","type":"string"}}`), "schemas.x.type", []string{"duplicate key"}},
		{"duplicate key inside inline json", minimal(`,"sources":{"s":{"decode":"json","inline":{"a":[{"b":1,"b":2}]}}}`), "sources.s.inline.a[0].b", []string{"duplicate key"}},
		{"duplicate key in a layer", minimal(`,"layers":[{"id":"a","id":"b"}]`), "layers[0].id", []string{"duplicate key"}},
		{"wrong type", minimal(`,"layers":[{"max_rounds":"3"}]`), "layers[0].max_rounds", []string{"want an integer", "string"}},
		{"wrong type in map entry", minimal(`,"participants":{"alice":{"agent":5}}`), "participants.alice.agent", []string{"want a string"}},
		{"wrong type boolean", minimal(`,"layers":[{"enabled":"yes"}]`), "layers[0].enabled", []string{"want a boolean"}},
		{"wrong type array", minimal(`,"result_layers":"report"`), "result_layers", []string{"want an array"}},
		{"wrong type object", minimal(`,"brief":"x"`), "brief", []string{"want an object"}},
		{"fractional seed", minimal(`,"seed":1.5`), "seed", []string{"want an integer"}},
		{"missing version", `{}`, "version", []string{"version 0 is not supported", "want 1"}},
		{"future version", `{"version":2}`, "version", []string{"version 2 is not supported"}},
		{"explicit zero layer max_calls", minimal(`,"layers":[{"id":"a"},{"id":"b","max_calls":0}]`), "layers[1].max_calls", []string{"positive", "omit it"}},
		{"explicit null share", minimal(`,"layers":[{"id":"a"},{"id":"b","output":{"format":"json","share":null}}]`), "layers[1].output.share", []string{"not null", "omit it"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Decode([]byte(tt.in))
			decodes := tt.path == "version" || strings.Contains(tt.name, "explicit")
			if (cfg != nil) != decodes {
				t.Fatalf("Decode returned configuration %v for invalid input, want one: %v", cfg != nil, decodes)
			}
			cfgtWantIssue(t, err, tt.path, tt.want...)
		})
	}
}

func TestDecodeReportsEveryStructuralIssue(t *testing.T) {
	in := `{"version":1,"bogus":1,"participants":{"alice":{"agent":"alice","x":1},"alice":{}},"name":"a","name":"b"}`
	_, err := Decode([]byte(in))
	ve, ok := errors.AsType[*ValidationError](err)
	if !ok {
		t.Fatalf("want *ValidationError, got %v", err)
	}
	if len(ve.Issues) != 4 {
		t.Fatalf("want 4 issues (bogus, x, duplicate alice, duplicate name), got %d:\n%v", len(ve.Issues), ve)
	}
}

func TestDecodeAccepts(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		check func(t *testing.T, c *Config)
	}{
		{"absent layer max_calls is zero", `{"version":1,"layers":[{"id":"a"}]}`, func(t *testing.T, c *Config) {
			t.Helper()
			if c.Layers[0].MaxCalls != 0 {
				t.Errorf("max_calls = %d", c.Layers[0].MaxCalls)
			}
		}},
		{"positive layer max_calls", `{"version":1,"layers":[{"id":"a","max_calls":4}]}`, func(t *testing.T, c *Config) {
			t.Helper()
			if c.Layers[0].MaxCalls != 4 {
				t.Errorf("max_calls = %d", c.Layers[0].MaxCalls)
			}
		}},
		{"share omitted vs empty", `{"version":1,"layers":[{"id":"a","output":{"format":"json"}},{"id":"b","output":{"format":"json","share":[]}}]}`, func(t *testing.T, c *Config) {
			t.Helper()
			if c.Layers[0].Output.Share != nil {
				t.Error("omitted share should be nil")
			}
			if c.Layers[1].Output.Share == nil || len(*c.Layers[1].Output.Share) != 0 {
				t.Error("share: [] should be a non-nil empty list")
			}
		}},
		{"enabled false and explicit seed", `{"version":1,"seed":-7,"layers":[{"id":"a","enabled":false}]}`, func(t *testing.T, c *Config) {
			t.Helper()
			if c.Layers[0].IsEnabled() || c.Seed == nil || *c.Seed != -7 {
				t.Errorf("enabled=%v seed=%v", c.Layers[0].IsEnabled(), c.Seed)
			}
		}},
		{"inline json kept verbatim", `{"version":1,"sources":{"s":{"decode":"json","inline":{"b":1,"a":[1,2]}}}}`, func(t *testing.T, c *Config) {
			t.Helper()
			if got := string(c.Sources["s"].Inline); got != `{"b":1,"a":[1,2]}` {
				t.Errorf("inline = %s", got)
			}
		}},
		{"same key in different objects", `{"version":1,"participants":{"alice":{"name":"x"},"bob":{"name":"x"}}}`, nil},
		{"large number in a schema", `{"version":1,"schemas":{"x":{"maximum":1e400}}}`, nil},
		{"surrounding whitespace", " \n{\"version\":1}\n\t ", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Decode([]byte(tt.in))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}

func TestCheckDuplicateKeys(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path string // "" with ok
		ok   bool
	}{
		{"scalar", `3`, "", true},
		{"nested distinct", `{"a":{"a":{"a":1}},"b":[{"a":1},{"a":2}]}`, "", true},
		{"root duplicate", `{"a":1,"a":2}`, "a", false},
		{"inside array", `{"x":[{},{"b":1,"b":1}]}`, "x[1].b", false},
		{"deep", `[[{"k":{"z":0,"z":0}}]]`, "[0][0].k.z", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkDuplicateKeys([]byte(tt.in))
			if tt.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			cfgtWantIssue(t, err, tt.path, "duplicate key")
		})
	}
	if err := checkDuplicateKeys([]byte(`{"a":`)); err == nil || !strings.Contains(err.Error(), "malformed JSON") {
		t.Errorf("malformed input: got %v", err)
	}
}

func TestIndexPath(t *testing.T) {
	for in, want := range map[string]string{
		"":                               "",
		"seed":                           "seed",
		"layers.0.max_rounds":            "layers[0].max_rounds",
		"layers.12.inputs.3.to":          "layers[12].inputs[3].to",
		"participants.alice.agent":       "participants.alice.agent",
		"layers.1.moderator.after_round": "layers[1].moderator.after_round",
	} {
		if got := indexPath(in); got != want {
			t.Errorf("indexPath(%q) = %q, want %q", in, got, want)
		}
	}
}
