package utils

import "testing"

func TestDisplayWidth(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"abc", 3},
		{"", 0},
		{"☀️", 2}, // U+2600 + U+FE0F (2 runes) → width 2
		{"🌙", 2},  // U+1F319 (1 rune) → width 2
		{"世界", 4}, // CJK wide
		{"️", 0},
		{"‍", 0},
		// The reported bug: mixed-emoji cells must measure equal so columns align.
		{"☀️ Morning", 10},
		{"🌙 Evening", 10},
	}
	for _, c := range cases {
		if got := DisplayWidth(c.s); got != c.want {
			t.Errorf("DisplayWidth(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}
