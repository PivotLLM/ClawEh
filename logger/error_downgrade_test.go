// ClawEh
// License: MIT

package logger

import "testing"

func TestErrorDowngrade(t *testing.T) {
	t.Run("no predicate keeps ERROR", func(t *testing.T) {
		l := NewLogger("x")
		if got := l.errorLevel("anything"); got != ERROR {
			t.Fatalf("errorLevel without predicate = %v, want ERROR", got)
		}
	})

	t.Run("predicate match downgrades to WARN", func(t *testing.T) {
		l := NewLogger("x").WithErrorDowngrade(func(msg string) bool { return msg == "transient" })
		if got := l.errorLevel("transient"); got != WARN {
			t.Fatalf("errorLevel(match) = %v, want WARN", got)
		}
		if got := l.errorLevel("fatal"); got != ERROR {
			t.Fatalf("errorLevel(no match) = %v, want ERROR", got)
		}
	})

	t.Run("nil predicate via WithErrorDowngrade keeps ERROR", func(t *testing.T) {
		l := NewLogger("x").WithErrorDowngrade(nil)
		if got := l.errorLevel("transient"); got != ERROR {
			t.Fatalf("errorLevel with nil predicate = %v, want ERROR", got)
		}
	})
}
