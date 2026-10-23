// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		"alice": true, "Bob": true, "_x": true, "-x": true, "a1": true, "a_b-c": true,
		"": false, "1a": false, "9": false, "a.b": false, "a b": false, "a/b": false, "é": false, "a:b": false,
	} {
		if got := validID(id); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestFormatExtension(t *testing.T) {
	for f, want := range map[Format]string{FormatText: ".txt", FormatMarkdown: ".md", FormatJSON: ".json", "other": ".txt"} {
		if got := f.Extension(); got != want {
			t.Errorf("%q.Extension() = %q, want %q", f, got, want)
		}
	}
}

func TestParticipantForm(t *testing.T) {
	tests := []struct {
		p    Participant
		want ParticipantForm
	}{
		{Participant{Agent: "alice"}, FormExisting},
		{Participant{Clone: "bob"}, FormClone},
		{Participant{Clone: "bob", Model: "large"}, FormClone},
		{Participant{Model: "default"}, FormFresh},
		{Participant{Agent: "alice", Clone: "bob"}, FormExisting},
	}
	for _, tt := range tests {
		if got := tt.p.Form(); got != tt.want {
			t.Errorf("%+v.Form() = %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestRouteProducer(t *testing.T) {
	tests := []struct {
		from    string
		kind    RouteKind
		id      string
		wantErr string
	}{
		{"source:report", RouteFromSource, "report", ""},
		{"layer:review", RouteFromLayer, "review", ""},
		{"layer:a:b", RouteFromLayer, "a:b", ""},
		{"report", "", "", "want"},
		{"source:", "", "", "want"},
		{"", "", "", "want"},
		{"file:x", "", "", `unknown producer kind "file"`},
	}
	for _, tt := range tests {
		kind, id, err := Route{From: tt.from}.Producer()
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Producer(%q) err = %v, want %q", tt.from, err, tt.wantErr)
			}
			continue
		}
		if err != nil || kind != tt.kind || id != tt.id {
			t.Errorf("Producer(%q) = %q, %q, %v", tt.from, kind, id, err)
		}
	}
}

func TestConfigLayerAccessors(t *testing.T) {
	cfg := cfgtExample(t)
	off := false
	cfg.Layers[2].Enabled = &off

	enabled := cfg.EnabledLayers()
	ids := make([]string, 0, len(enabled))
	for _, l := range enabled {
		ids = append(ids, l.ID)
	}
	if !slices.Equal(ids, []string{"review", "debate"}) {
		t.Errorf("EnabledLayers = %v", ids)
	}
	if l, ok := cfg.Layer("debate"); !ok || l.ID != "debate" {
		t.Errorf("Layer(debate) = %v, %v", l.ID, ok)
	}
	if _, ok := cfg.Layer("missing"); ok {
		t.Error("Layer(missing) found")
	}
	if got := cfg.EffectiveResultLayers(); !slices.Equal(got, []string{"report"}) {
		t.Errorf("explicit result layers = %v", got)
	}
	cfg.ResultLayers = nil
	if got := cfg.EffectiveResultLayers(); !slices.Equal(got, []string{"debate"}) {
		t.Errorf("default result layer = %v, want the last enabled layer", got)
	}
	for i := range cfg.Layers {
		cfg.Layers[i].Enabled = &off
	}
	if got := cfg.EffectiveResultLayers(); got != nil {
		t.Errorf("all disabled: %v, want nil", got)
	}
}

func TestEffectiveModeratorSchemaErrors(t *testing.T) {
	if _, err := effectiveModeratorSchema(Layer{ID: "debate"}, nil); err == nil || !strings.Contains(err.Error(), `"debate"`) {
		t.Errorf("no moderator: %v", err)
	}
	layer := Layer{ID: "debate", Moderator: &Moderator{Participant: "chair", Schema: "notes"}}
	for _, bad := range []string{`[]`, `"x"`, `true`, `null`, `{`} {
		_, err := effectiveModeratorSchema(layer, cfgtRaw(bad))
		if err == nil || !strings.Contains(err.Error(), `"notes" is not a JSON object`) {
			t.Errorf("assessment %s: %v", bad, err)
		}
	}
}

func TestEffectiveModeratorSchemaShape(t *testing.T) {
	layer := Layer{ID: "debate", Participants: []string{"alice", "bob"}, Moderator: &Moderator{Participant: "chair"}}
	raw, err := effectiveModeratorSchema(layer, nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Type                 string                     `json:"type"`
		AdditionalProperties bool                       `json:"additionalProperties"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Type != "object" || doc.AdditionalProperties || !slices.Equal(doc.Required, []string{"decision", "reason", "guidance"}) {
		t.Errorf("schema = %s", raw)
	}
	if _, ok := doc.Properties["assessment"]; ok {
		t.Error("assessment present without a moderator schema")
	}
	if _, ok := doc.Properties["directed"]; ok {
		t.Error("directed present without allow_directed")
	}
	again, err := effectiveModeratorSchema(layer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(raw) {
		t.Error("EffectiveModeratorSchema is not deterministic")
	}
}

func TestEffectiveModeratorSchemaValidates(t *testing.T) {
	const assessment = `{"type":"object","required":["open_issues"],"additionalProperties":false,
		"properties":{"open_issues":{"type":"array","items":{"$ref":"#/$defs/issue"}}},
		"$defs":{"issue":{"type":"string","minLength":1}}}`
	plain := Layer{ID: "debate", Participants: []string{"alice", "bob"}, Moderator: &Moderator{Participant: "chair"}}
	assessed := plain
	assessed.Moderator = &Moderator{Participant: "chair", Schema: "notes"}
	directed := plain
	directed.Moderator = &Moderator{Participant: "chair", AllowDirected: true}

	tests := []struct {
		name     string
		layer    Layer
		decision string
		ok       bool
	}{
		{"continue", plain, `{"decision":"CONTINUE","reason":"fine","guidance":null}`, true},
		{"stop", plain, `{"decision":"STOP","reason":"done","guidance":null}`, true},
		{"guide", plain, `{"decision":"GUIDE","reason":"r","guidance":"Examine rollback."}`, true},
		{"continue with guidance", plain, `{"decision":"CONTINUE","reason":"r","guidance":"x"}`, false},
		{"stop with guidance", plain, `{"decision":"STOP","reason":"r","guidance":"x"}`, false},
		{"guide with null guidance", plain, `{"decision":"GUIDE","reason":"r","guidance":null}`, false},
		{"guide with empty guidance", plain, `{"decision":"GUIDE","reason":"r","guidance":""}`, false},
		{"guide with blank guidance", plain, `{"decision":"GUIDE","reason":"r","guidance":" \n\t "}`, false},
		{"guide with padded guidance", plain, `{"decision":"GUIDE","reason":"r","guidance":"  Examine rollback.  "}`, true},
		{"unknown decision", plain, `{"decision":"PAUSE","reason":"r","guidance":null}`, false},
		{"lowercase decision", plain, `{"decision":"continue","reason":"r","guidance":null}`, false},
		{"missing reason", plain, `{"decision":"CONTINUE","guidance":null}`, false},
		{"missing guidance", plain, `{"decision":"CONTINUE","reason":"r"}`, false},
		{"non-string reason", plain, `{"decision":"CONTINUE","reason":1,"guidance":null}`, false},
		{"extra field", plain, `{"decision":"CONTINUE","reason":"r","guidance":null,"mood":"ok"}`, false},
		{"assessment without schema", plain, `{"decision":"CONTINUE","reason":"r","guidance":null,"assessment":{}}`, false},
		{"not an object", plain, `["CONTINUE"]`, false},
		{"assessment valid", assessed, `{"decision":"CONTINUE","reason":"r","guidance":null,"assessment":{"open_issues":["Rollback"]}}`, true},
		{"assessment missing", assessed, `{"decision":"CONTINUE","reason":"r","guidance":null}`, false},
		{"assessment invalid via internal ref", assessed, `{"decision":"CONTINUE","reason":"r","guidance":null,"assessment":{"open_issues":[""]}}`, false},
		{"assessment closed", assessed, `{"decision":"CONTINUE","reason":"r","guidance":null,"assessment":{"open_issues":[],"x":1}}`, false},
		{"directed valid", directed, `{"decision":"GUIDE","reason":"r","guidance":"g","directed":[{"to":"alice","text":"Press on cost."},{"to":"bob","text":"Be brief."}]}`, true},
		{"directed optional", directed, `{"decision":"CONTINUE","reason":"r","guidance":null}`, true},
		{"directed to the moderator", directed, `{"decision":"CONTINUE","reason":"r","guidance":null,"directed":[{"to":"chair","text":"x"}]}`, false},
		{"directed to an outsider", directed, `{"decision":"CONTINUE","reason":"r","guidance":null,"directed":[{"to":"carol","text":"x"}]}`, false},
		{"directed empty text", directed, `{"decision":"CONTINUE","reason":"r","guidance":null,"directed":[{"to":"alice","text":""}]}`, false},
		{"directed missing text", directed, `{"decision":"CONTINUE","reason":"r","guidance":null,"directed":[{"to":"alice"}]}`, false},
		{"directed extra field", directed, `{"decision":"CONTINUE","reason":"r","guidance":null,"directed":[{"to":"alice","text":"x","cc":"bob"}]}`, false},
		{"directed not allowed", plain, `{"decision":"CONTINUE","reason":"r","guidance":null,"directed":[{"to":"alice","text":"x"}]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a json.RawMessage
			if tt.layer.Moderator.Schema != "" {
				a = cfgtRaw(assessment)
			}
			raw, err := effectiveModeratorSchema(tt.layer, a)
			if err != nil {
				t.Fatal(err)
			}
			sch, err := compileSchema(raw)
			if err != nil {
				t.Fatalf("effective schema does not compile: %v\n%s", err, raw)
			}
			err = sch.Validate([]byte(tt.decision))
			if tt.ok && err != nil {
				t.Errorf("rejected: %v", err)
			}
			if !tt.ok {
				if isViolation := errors.As(err, new(*SchemaViolationError)); !isViolation {
					t.Errorf("want a schema violation, got %v", err)
				}
			}
		})
	}
}

func TestEffectiveModeratorSchemaKeepsAssessmentID(t *testing.T) {
	layer := Layer{ID: "debate", Moderator: &Moderator{Participant: "chair", Schema: "notes"}}
	raw, err := effectiveModeratorSchema(layer, cfgtRaw(`{"$id":"forum:///notes.json","type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"$id":"forum:///notes.json"`) || strings.Contains(string(raw), assessmentSchemaID) {
		t.Errorf("the assessment's own $id must be kept: %s", raw)
	}
	if raw, err = effectiveModeratorSchema(layer, cfgtRaw(`{"type":"object"}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), assessmentSchemaID) {
		t.Errorf("an assessment without $id gets %s: %s", assessmentSchemaID, raw)
	}
}

func TestGuidanceIssue(t *testing.T) {
	text := func(s string) *string { return &s }
	tests := []struct {
		name string
		d    Decision
		want string // substring; "" means the rule holds
	}{
		{"guide", Decision{Decision: DecisionGuide, Guidance: text("Examine rollback.")}, ""},
		{"guide null", Decision{Decision: DecisionGuide}, "nonblank"},
		{"guide empty", Decision{Decision: DecisionGuide, Guidance: text("")}, "nonblank"},
		{"guide blank", Decision{Decision: DecisionGuide, Guidance: text(" \n\t")}, "nonblank"},
		{"continue null", Decision{Decision: DecisionContinue}, ""},
		{"continue with guidance", Decision{Decision: DecisionContinue, Guidance: text("x")}, "CONTINUE requires guidance to be null"},
		{"stop with guidance", Decision{Decision: DecisionStop, Guidance: text("")}, "STOP requires guidance to be null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := guidanceIssue(&tt.d)
			if tt.want == "" && got != "" || tt.want != "" && !strings.Contains(got, tt.want) {
				t.Errorf("guidanceIssue = %q, want %q", got, tt.want)
			}
		})
	}
}
