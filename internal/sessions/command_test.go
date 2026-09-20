// ClawEh - Session store CLI
// License: MIT

package sessions

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ctxengine/memory"
)

// A jsonl + meta.json pair the way the old store wrote them, folded into the
// session's archive DB with the sources renamed, and idempotent on re-run.
func TestMigrateDirs_FoldsJSONLIntoArchiveDB(t *testing.T) {
	dir := t.TempDir()
	const key = "agent:alice:webui:direct:webui:fixture"
	base := filepath.Join(dir, memory.SanitizeSessionKey(key))

	jsonl := strings.Join([]string{
		`{"seq":1,"created_at":"2026-09-01T10:00:00Z","role":"user","content":"gone (skipped)"}`,
		`{"seq":2,"created_at":"2026-09-01T10:00:01Z","role":"user","content":"hello"}`,
		`{"seq":3,"created_at":"2026-09-01T10:00:02Z","role":"assistant","content":"hi there"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(base+".jsonl", []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := `{
  "key": "` + key + `",
  "summary": "a short summary",
  "skip": 1,
  "count": 3,
  "created_at": "2026-09-01T10:00:00Z",
  "updated_at": "2026-09-01T10:00:02Z",
  "next_seq": 3,
  "meaningful_count": 2
}`
	if err := os.WriteFile(base+".meta.json", []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	// A directory that does not exist must be skipped silently.
	var out bytes.Buffer
	if err := migrateDirs(&out, []string{dir, filepath.Join(dir, "missing")}); err != nil {
		t.Fatalf("migrateDirs: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), key+": migrated") {
		t.Errorf("output missing per-session line:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Migrated 1, skipped 0, errors 0.") {
		t.Errorf("output missing totals line:\n%s", out.String())
	}

	// Sources renamed, DB present.
	for _, gone := range []string{base + ".jsonl", base + ".meta.json"} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s should have been renamed, stat err = %v", gone, err)
		}
	}
	for _, kept := range []string{base + ".jsonl.migrated", base + ".meta.json.migrated"} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s should exist: %v", kept, err)
		}
	}

	db, err := memory.OpenReadOnly(memory.ArchivePath(dir, key))
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close archive: %v", closeErr)
		}
	}()

	window, err := db.Window()
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	if len(window) != 2 {
		t.Fatalf("window rows = %d, want 2 (skip honoured): %+v", len(window), window)
	}
	if window[0].Seq != 2 || window[0].Content != "hello" {
		t.Errorf("window[0] = seq %d %q, want 2 hello", window[0].Seq, window[0].Content)
	}
	if window[1].Seq != 3 || window[1].Role != "assistant" {
		t.Errorf("window[1] = seq %d role %q, want 3 assistant", window[1].Seq, window[1].Role)
	}

	state, err := db.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.Key != key {
		t.Errorf("state.Key = %q, want %q", state.Key, key)
	}
	if state.Summary != "a short summary" {
		t.Errorf("state.Summary = %q", state.Summary)
	}
	if state.NextSeq != 3 {
		t.Errorf("state.NextSeq = %d, want 3", state.NextSeq)
	}
	if state.PendingTurn {
		t.Error("state.PendingTurn must be false after migration")
	}

	// Second run: nothing left to fold, nothing to report as an error.
	out.Reset()
	if err := migrateDirs(&out, []string{dir}); err != nil {
		t.Fatalf("second migrateDirs: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Migrated 0, skipped 0, errors 0.") {
		t.Errorf("second run totals:\n%s", out.String())
	}
}

// A .jsonl with no meta.json has no trustworthy key; it is reported, not
// guessed, and the command exits non-zero.
func TestMigrateDirs_ReportsOrphanJSONL(t *testing.T) {
	dir := t.TempDir()
	orphan := filepath.Join(dir, "agent_bob_main.jsonl")
	if err := os.WriteFile(orphan, []byte(`{"seq":1,"role":"user","content":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := migrateDirs(&out, []string{dir})
	if err == nil {
		t.Fatalf("expected an error for an orphan .jsonl\n%s", out.String())
	}
	if !strings.Contains(out.String(), "error") {
		t.Errorf("output should name the error:\n%s", out.String())
	}
	if _, statErr := os.Stat(orphan); statErr != nil {
		t.Errorf("orphan must be left untouched: %v", statErr)
	}
}
