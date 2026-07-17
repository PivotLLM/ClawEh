package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLog(t *testing.T, lines int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claw.log")
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString("line-")
		b.WriteString(strings.Repeat("x", 200)) // long-ish lines exercise the byte budget
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return path
}

func TestTailLines_FewerLinesThanFile(t *testing.T) {
	path := writeLog(t, 1000)
	got, err := tailLines(path, 250)
	if err != nil {
		t.Fatalf("tailLines: %v", err)
	}
	if len(got) != 250 {
		t.Fatalf("got %d lines, want 250", len(got))
	}
	// The tail must be the LAST 250 lines and every one must be complete (no
	// partial first line from a mid-file seek).
	for _, ln := range got {
		if !strings.HasPrefix(ln, "line-x") {
			t.Fatalf("partial/garbled line in tail: %q", ln[:min(20, len(ln))])
		}
	}
}

func TestTailLines_MoreRequestedThanFile(t *testing.T) {
	path := writeLog(t, 10)
	got, err := tailLines(path, 250)
	if err != nil {
		t.Fatalf("tailLines: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d lines, want 10 (whole file)", len(got))
	}
}

func TestTailLines_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claw.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := tailLines(path, 250)
	if err != nil {
		t.Fatalf("tailLines: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty file should yield 0 lines, got %d", len(got))
	}
}

func TestTailLines_MissingFile(t *testing.T) {
	if _, err := tailLines(filepath.Join(t.TempDir(), "nope.log"), 250); err == nil {
		t.Fatal("expected error for missing file")
	}
}
