package llmcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

func jsonDumps(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestCompactionRecorder_DumpsFailuresOnly verifies that with failureDumpDir set,
// a failed attempt writes a compress_fail dump (request + raw response) to
// logs/dumps, while a successful attempt writes nothing.
func TestCompactionRecorder_DumpsFailuresOnly(t *testing.T) {
	dir := t.TempDir()
	rec := &compactionRecorder{sessionKey: "sess-1", failureDumpDir: dir}

	req := []providers.Message{{Role: "user", Content: "summarize the conversation"}}
	rec.record("gpt-5.4", "error", "invalid JSON response", time.Second, req, "Sorry, I can't comply.")

	dumps := jsonDumps(t, dir)
	if len(dumps) != 1 {
		t.Fatalf("expected 1 failure dump, got %d (%v)", len(dumps), dumps)
	}
	data, err := os.ReadFile(filepath.Join(dir, dumps[0]))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{"compress_fail", "gpt-5.4", "invalid JSON response", "Sorry, I can't comply."} {
		if !strings.Contains(body, want) {
			t.Errorf("dump missing %q:\n%s", want, body)
		}
	}

	// A successful attempt must not add a dump.
	rec.record("gpt-5.4", "ok", "", time.Second, req, `{"version":2}`)
	if got := len(jsonDumps(t, dir)); got != 1 {
		t.Errorf("ok attempt should not dump; dump count = %d, want 1", got)
	}
}

// TestCompactionRecorder_NoDumpWhenDisabled verifies that with no failureDumpDir,
// failures are not dumped.
func TestCompactionRecorder_NoDumpWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	rec := &compactionRecorder{sessionKey: "sess-2"} // failureDumpDir empty
	rec.record("m", "error", "boom", time.Second, nil, "x")
	if got := len(jsonDumps(t, dir)); got != 0 {
		t.Errorf("expected no dumps when disabled, got %d", got)
	}
}
