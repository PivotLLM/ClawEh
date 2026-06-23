// ClawEh
// License: MIT

package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func readBack(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func rangeTool(op, unit, dir string) *rangeEditTool {
	return newRangeEditTool(op, unit, dir, false, "")
}

func TestEditLines_Replace(t *testing.T) {
	dir, p := writeTemp(t, "L1\nL2\nL3\n")
	res := rangeTool("edit", "lines", dir).Execute(context.Background(),
		map[string]any{"path": p, "start": 2, "end": 2, "replace": "X\nY", "backup": false})
	if res.IsError {
		t.Fatalf("err: %s", res.ForLLM)
	}
	if got := readBack(t, p); got != "L1\nX\nY\nL3\n" {
		t.Fatalf("got %q", got)
	}
}

func TestEditLines_EndOptionalToEOF(t *testing.T) {
	dir, p := writeTemp(t, "L1\nL2\nL3\n")
	rangeTool("edit", "lines", dir).Execute(context.Background(),
		map[string]any{"path": p, "start": 2, "replace": "Z", "backup": false})
	if got := readBack(t, p); got != "L1\nZ\n" {
		t.Fatalf("end-optional should replace to EOF, got %q", got)
	}
}

func TestEditLines_RefusesEmptyReplace(t *testing.T) {
	dir, p := writeTemp(t, "L1\nL2\n")
	res := rangeTool("edit", "lines", dir).Execute(context.Background(),
		map[string]any{"path": p, "start": 1, "replace": "", "backup": false})
	if !res.IsError || !contains(res.ForLLM, "file_delete_lines") {
		t.Fatalf("want refusal pointing at file_delete_lines, got: %s", res.ForLLM)
	}
	if readBack(t, p) != "L1\nL2\n" {
		t.Fatalf("file must be unchanged after refusal")
	}
}

func TestInsertLines_AfterLine(t *testing.T) {
	dir, p := writeTemp(t, "L1\nL2\n")
	rangeTool("insert", "lines", dir).Execute(context.Background(),
		map[string]any{"path": p, "after_line": 1, "text": "NEW"})
	if got := readBack(t, p); got != "L1\nNEW\nL2\n" {
		t.Fatalf("got %q", got)
	}
}

func TestInsertLines_Top(t *testing.T) {
	dir, p := writeTemp(t, "L1\nL2\n")
	rangeTool("insert", "lines", dir).Execute(context.Background(),
		map[string]any{"path": p, "after_line": 0, "text": "TOP"})
	if got := readBack(t, p); got != "TOP\nL1\nL2\n" {
		t.Fatalf("got %q", got)
	}
}

func TestDeleteLines_Range(t *testing.T) {
	dir, p := writeTemp(t, "L1\nL2\nL3\nL4\n")
	rangeTool("delete", "lines", dir).Execute(context.Background(),
		map[string]any{"path": p, "start": 2, "end": 3, "backup": false})
	if got := readBack(t, p); got != "L1\nL4\n" {
		t.Fatalf("got %q", got)
	}
}

func TestEditBytes_ReplaceSingleByte(t *testing.T) {
	dir, p := writeTemp(t, "abcdef")
	// start=0,end=0 replaces exactly byte 0 (the byte-0 case the optional end fixes).
	rangeTool("edit", "bytes", dir).Execute(context.Background(),
		map[string]any{"path": p, "start": 0, "end": 0, "replace": "Z", "backup": false})
	if got := readBack(t, p); got != "Zbcdef" {
		t.Fatalf("got %q", got)
	}
}

func TestEditBytes_EndOptionalToEOF(t *testing.T) {
	dir, p := writeTemp(t, "abcdef")
	rangeTool("edit", "bytes", dir).Execute(context.Background(),
		map[string]any{"path": p, "start": 3, "replace": "XYZ", "backup": false})
	if got := readBack(t, p); got != "abcXYZ" {
		t.Fatalf("end-optional should replace to EOF, got %q", got)
	}
}

func TestInsertBytes_AtOffset(t *testing.T) {
	dir, p := writeTemp(t, "abcdef")
	rangeTool("insert", "bytes", dir).Execute(context.Background(),
		map[string]any{"path": p, "at_offset": 3, "text": "--"})
	if got := readBack(t, p); got != "abc--def" {
		t.Fatalf("got %q", got)
	}
}

func TestDeleteBytes_Range(t *testing.T) {
	dir, p := writeTemp(t, "abcdef")
	rangeTool("delete", "bytes", dir).Execute(context.Background(),
		map[string]any{"path": p, "start": 1, "end": 3, "backup": false})
	if got := readBack(t, p); got != "aef" {
		t.Fatalf("got %q", got)
	}
}

func TestRangeEdit_Names_And_BackupDefaults(t *testing.T) {
	cases := []struct {
		op, unit, name string
		backup         bool
	}{
		{"edit", "lines", "file_edit_lines", true},
		{"edit", "bytes", "file_edit_bytes", true},
		{"delete", "lines", "file_delete_lines", true},
		{"delete", "bytes", "file_delete_bytes", true},
		{"insert", "lines", "file_insert_lines", false},
		{"insert", "bytes", "file_insert_bytes", false},
	}
	for _, c := range cases {
		tl := &rangeEditTool{op: c.op, unit: c.unit}
		if tl.Name() != c.name {
			t.Errorf("Name() = %q, want %q", tl.Name(), c.name)
		}
		if tl.backupDefault() != c.backup {
			t.Errorf("%s backupDefault = %v, want %v", c.name, tl.backupDefault(), c.backup)
		}
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
