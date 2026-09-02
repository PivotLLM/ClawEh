package files

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func countFixture(t *testing.T, content string) (*CountFileTool, string, string) {
	t.Helper()
	dir := t.TempDir()
	name := "sample.txt"
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewCountFileTool(dir, true), name, full
}

func runCount(t *testing.T, tool *CountFileTool, path string) fileCounts {
	t.Helper()
	res := tool.Execute(t.Context(), map[string]any{"path": path})
	if res == nil || res.IsError {
		t.Fatalf("file_count failed: %+v", res)
	}
	var got fileCounts
	if err := json.Unmarshal([]byte(res.ForLLM), &got); err != nil {
		t.Fatalf("result is not JSON: %v (%s)", err, res.ForLLM)
	}
	return got
}

// TestCount_MatchesWc checks the equivalence claim against the real wc rather
// than against my own reading of its manual. Skipped where wc is unavailable so
// the suite stays portable.
func TestCount_MatchesWc(t *testing.T) {
	wcPath, err := exec.LookPath("wc")
	if err != nil {
		t.Skip("wc not available")
	}

	for _, tc := range []struct {
		name    string
		content string
	}{
		{"simple lines", "alpha bravo\ncharlie\n"},
		{"no trailing newline", "alpha bravo\ncharlie"},
		{"empty file", ""},
		{"only newlines", "\n\n\n"},
		{"runs of whitespace", "  alpha \t\t bravo  \n\n  charlie  \n"},
		{"leading and trailing blanks", "\n\n  word  \n\n"},
		{"single word no newline", "word"},
		{"multibyte text", "héllo wörld\nnaïve café\n"},
		{"cjk", "世界 こんにちは\n"},
		{"tabs as separators", "a\tb\tc\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool, rel, full := countFixture(t, tc.content)
			got := runCount(t, tool, rel)

			// wc -l -w -m -c prints the counts in that order.
			out, err := exec.Command(wcPath, "-l", "-w", "-m", "-c", full).Output()
			if err != nil {
				t.Fatalf("wc failed: %v", err)
			}
			nums := strings.Fields(string(out))
			if len(nums) < 4 {
				t.Fatalf("unexpected wc output: %q", out)
			}
			want := make([]int64, 4)
			for i := range want {
				v, perr := strconv.ParseInt(nums[i], 10, 64)
				if perr != nil {
					t.Fatalf("parsing wc output %q: %v", nums[i], perr)
				}
				want[i] = v
			}

			for _, cmp := range []struct {
				label     string
				got, want int64
			}{
				{"lines", got.Lines, want[0]},
				{"words", got.Words, want[1]},
				{"characters", got.Characters, want[2]},
				{"bytes", got.Bytes, want[3]},
			} {
				if cmp.got != cmp.want {
					t.Errorf("%s = %d, wc says %d (content %q)", cmp.label, cmp.got, cmp.want, tc.content)
				}
			}
		})
	}
}

// TestCount_FinalNewline pins the flag that explains wc's line count. Without it
// a caller cannot tell "one line, no trailing newline" from "one line plus a
// newline", which is the discrepancy that makes wc -l surprising.
func TestCount_FinalNewline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		lines   int64
		final   bool
	}{
		{"terminated", "a\nb\n", 2, true},
		{"unterminated", "a\nb", 1, false},
		{"empty is treated as terminated", "", 0, true},
		{"single newline", "\n", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool, rel, _ := countFixture(t, tc.content)
			got := runCount(t, tool, rel)
			if got.Lines != tc.lines || got.FinalNewline != tc.final {
				t.Fatalf("lines=%d final_newline=%v, want %d/%v", got.Lines, got.FinalNewline, tc.lines, tc.final)
			}
		})
	}
}

// TestCount_MultibyteSplitsCharsFromBytes is the case that makes reporting both
// worth it.
func TestCount_MultibyteSplitsCharsFromBytes(t *testing.T) {
	tool, rel, _ := countFixture(t, "héllo\n") // é is two bytes in UTF-8
	got := runCount(t, tool, rel)
	if got.Characters != 6 {
		t.Errorf("characters = %d, want 6", got.Characters)
	}
	if got.Bytes != 7 {
		t.Errorf("bytes = %d, want 7", got.Bytes)
	}
	if got.InvalidUTF8 {
		t.Error("valid UTF-8 flagged as invalid")
	}
}

// TestCount_InvalidUTF8IsFlaggedNotFatal keeps binary files measurable, with a
// marker saying the character count cannot be trusted.
func TestCount_InvalidUTF8IsFlaggedNotFatal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte{0xff, 0xfe, 'a', '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	got := runCount(t, NewCountFileTool(dir, true), "bin")
	if !got.InvalidUTF8 {
		t.Error("invalid UTF-8 was not flagged")
	}
	if got.Bytes != 4 {
		t.Errorf("bytes = %d, want 4 — byte count must stay meaningful", got.Bytes)
	}
	if got.Lines != 1 {
		t.Errorf("lines = %d, want 1", got.Lines)
	}
}

// TestCount_Errors covers the argument and path-policy failures.
func TestCount_Errors(t *testing.T) {
	tool, _, _ := countFixture(t, "x\n")

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"missing path", map[string]any{}},
		{"empty path", map[string]any{"path": ""}},
		{"wrong type", map[string]any{"path": 42}},
		{"no such file", map[string]any{"path": "absent.txt"}},
		{"escapes the workspace", map[string]any{"path": "../../etc/passwd"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := tool.Execute(t.Context(), tc.args)
			if res == nil || !res.IsError {
				t.Fatalf("expected an error result, got %+v", res)
			}
		})
	}
}
