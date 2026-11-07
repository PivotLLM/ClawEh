// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A classifier tool named as an action is refused on save (create and
// validate) and in a hand-edited file, which is then not run; the same
// definition with an ordinary tool is accepted.
func TestClassifierToolsRefused(t *testing.T) {
	e := newEnv(t)
	e.tools.set("classifier_update", func(map[string]any) (string, error) { return "", nil })
	def := definition()
	obj(obj(def["actions"])["urgent"])["tool"] = "classifier_update"
	const want = "classifier_update can't be used by a classifier (actions.urgent)."

	if _, err := e.svc.Create(context.Background(), e.scope, mustJSON(t, def)); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Create = %v, want %q", err, want)
	}
	if err := e.svc.Validate(context.Background(), e.scope, mustJSON(t, def), true); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Validate = %v, want %q", err, want)
	}

	// The ordinary tool is accepted.
	e.create(definition())
	// An update that names a classifier tool is refused and changes nothing.
	patch := []byte(`{"source":{"tool":"classifier_list"}}`)
	if err := e.svc.Update(context.Background(), e.scope, "inbox-priority", patch); err == nil ||
		!strings.Contains(err.Error(), "classifier_list can't be used by a classifier (source).") {
		t.Fatalf("Update = %v", err)
	}

	// A hand edit naming one leaves the file invalid: not run, kept.
	path := filepath.Join(e.scope.BaseDirectory, "inbox-priority.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), `"tool": "mail_read"`, `"tool": "classifier_get"`, 1)
	if edited == string(raw) {
		t.Fatal("fixture: no fetch tool to edit")
	}
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	fi := readFile(path, Ceilings{})
	if fi.def != nil || !strings.Contains(fi.invalid, "classifier_get can't be used by a classifier (fetch).") {
		t.Fatalf("hand-edited file: def=%v invalid=%q", fi.def != nil, fi.invalid)
	}
}

func TestIsClassifierTool(t *testing.T) {
	for tool, want := range map[string]bool{
		"classifier_list": true, "Classifier_Update": true,
		"mail_list": false, "classifierx_list": false, "my_classifier_list": false,
	} {
		if got := IsClassifierTool(tool); got != want {
			t.Errorf("IsClassifierTool(%q) = %v, want %v", tool, got, want)
		}
	}
}
