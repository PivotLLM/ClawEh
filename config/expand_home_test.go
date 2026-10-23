package config

import "testing"

func TestExpandHome(t *testing.T) {
	t.Setenv("HOME", "/home/alice")
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"absolute", "/srv/claw", "/srv/claw"},
		{"relative", "agents/bob", "agents/bob"},
		{"tilde only", "~", "/home/alice"},
		{"tilde slash", "~/agents/bob", "/home/alice/agents/bob"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExpandHome(tc.in); got != tc.want {
				t.Fatalf("ExpandHome(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// With no home directory the path is returned unchanged, never rooted at "/".
func TestExpandHome_HomeUnknown(t *testing.T) {
	t.Setenv("HOME", "")
	if got := ExpandHome("~/agents/bob"); got != "~/agents/bob" {
		t.Fatalf("ExpandHome with no home = %q, want the path unchanged", got)
	}
}
