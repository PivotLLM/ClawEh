// ClawEh
// License: MIT

package logger

import "testing"

// TestErrorHook: the hook sees every Error/Errorf that stays at ERROR and
// none that the downgrade predicate demotes.
func TestErrorHook(t *testing.T) {
	var got []string
	l := NewLogger("x").
		WithErrorDowngrade(func(msg string) bool { return msg == "transient" }).
		WithErrorHook(func(msg string) { got = append(got, msg) })

	l.Error("transient")
	l.Errorf("%s", "transient")
	l.Error("fatal")
	l.Errorf("code %d", 401)

	if len(got) != 2 || got[0] != "fatal" || got[1] != "code 401" {
		t.Fatalf("hook must fire only for non-downgraded errors, got %q", got)
	}

	l.WithErrorHook(nil)
	l.Error("fatal")
	if len(got) != 2 {
		t.Fatalf("nil hook must clear it, got %q", got)
	}
}
