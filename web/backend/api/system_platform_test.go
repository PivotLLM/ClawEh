package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrettyName(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "quoted value",
			path: write("ubuntu", "NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.4 LTS\"\nID=ubuntu\n"),
			want: "Ubuntu 24.04.4 LTS",
		},
		{
			name: "unquoted value",
			path: write("bare", "PRETTY_NAME=Alpine Linux v3.20\n"),
			want: "Alpine Linux v3.20",
		},
		{
			// A host with no PRETTY_NAME must yield "", so the caller falls back
			// to the Go platform string rather than printing a blank line.
			name: "no pretty name",
			path: write("none", "NAME=Whatever\n"),
			want: "",
		},
		{
			name: "missing file",
			path: filepath.Join(dir, "absent"),
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := prettyName(tc.path); got != tc.want {
				t.Errorf("prettyName = %q, want %q", got, tc.want)
			}
		})
	}
}
