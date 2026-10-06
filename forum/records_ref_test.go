// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import "testing"

// Ref names a forum by "<name> (<id>)", or by its ID when it has no name
// (a Label of an unnamed forum is its ID).
func TestRef(t *testing.T) {
	for _, tc := range []struct{ name, id, want string }{
		{"writing", "f1", "writing (f1)"},
		{"", "f1", "f1"},
		{"f1", "f1", "f1"},
	} {
		if got := Ref(tc.name, tc.id); got != tc.want {
			t.Errorf("Ref(%q, %q) = %q, want %q", tc.name, tc.id, got, tc.want)
		}
	}
}
