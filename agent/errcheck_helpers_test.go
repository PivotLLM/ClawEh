package agent

import (
	"io"
	"os"
	"testing"

	"github.com/PivotLLM/ctxengine/session"

	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/providers"
)

// removeAll removes path and reports a failure instead of silently leaking
// the temp tree.
func removeAll(tb testing.TB, path string) {
	tb.Helper()
	if err := os.RemoveAll(path); err != nil {
		tb.Errorf("RemoveAll(%s): %v", path, err)
	}
}

// closeT closes c and reports a close failure on the test.
func closeT(tb testing.TB, c io.Closer) {
	tb.Helper()
	if err := c.Close(); err != nil {
		tb.Errorf("Close: %v", err)
	}
}

// addMessage appends a role/content message to key in store or fails the test.
func addMessage(tb testing.TB, store session.SessionStore, key, role, content string) {
	tb.Helper()
	if err := store.AddMessage(key, role, content); err != nil {
		tb.Fatalf("AddMessage(%s): %v", key, err)
	}
}

// addFullMessage appends m to key in store or fails the test.
func addFullMessage(tb testing.TB, store session.SessionStore, key string, m providers.Message) {
	tb.Helper()
	if _, err := store.AddFullMessage(key, m); err != nil {
		tb.Fatalf("AddFullMessage(%s): %v", key, err)
	}
}

// setHistory replaces key's history in store or fails the test.
func setHistory(tb testing.TB, store session.SessionStore, key string, history []providers.Message) {
	tb.Helper()
	if err := store.SetHistory(key, history); err != nil {
		tb.Fatalf("SetHistory(%s): %v", key, err)
	}
}

// setSummary stores key's summary in store or fails the test.
func setSummary(tb testing.TB, store session.SessionStore, key, summary string) {
	tb.Helper()
	if err := store.SetSummary(key, summary); err != nil {
		tb.Fatalf("SetSummary(%s): %v", key, err)
	}
}

// mustStore stores localPath in the media store or fails the test.
func mustStore(t *testing.T, store *media.FileMediaStore, localPath string, meta media.MediaMeta) string {
	t.Helper()
	ref, err := store.Store(localPath, meta, "test")
	if err != nil {
		t.Fatalf("Store(%s): %v", localPath, err)
	}
	return ref
}
