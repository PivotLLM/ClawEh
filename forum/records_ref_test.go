// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"strings"
	"testing"
)

// Ref names a forum by "<name> (<id>)", or by its ID when it has no name
// (a Label of an unnamed forum is its ID).
func TestRef(t *testing.T) {
	for _, tc := range []struct{ name, id, want string }{
		{"writing", "f1", "writing (f1)"},
		{"", "f1", "f1"},
		{"f1", "f1", "f1"},
		{"two\nlines\x1b[31m", "f1", "twolines[31m (f1)"},
		{"\n\t", "f1", "f1"},
		{strings.Repeat("é", MaxNameChars+5), "f1", strings.Repeat("é", MaxNameChars) + " (f1)"},
	} {
		if got := Ref(tc.name, tc.id); got != tc.want {
			t.Errorf("Ref(%q, %q) = %q, want %q", tc.name, tc.id, got, tc.want)
		}
	}
}

// A forum name must be one line of at most MaxNameChars characters
// without control characters.
func TestValidateStaticName(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"", true},
		{"design-review", true},
		{strings.Repeat("é", MaxNameChars), true},
		{strings.Repeat("a", MaxNameChars+1), false},
		{"two\nlines", false},
		{"tab\there", false},
		{"bell\x07", false},
	} {
		cfg := ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText))
		cfg.Name = tc.name
		err := ValidateStatic(cfg)
		bad := err != nil && strings.Contains(err.Error(), "name: must be one line of at most 100 characters")
		if bad == tc.ok {
			t.Errorf("name %q: ValidateStatic = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}
