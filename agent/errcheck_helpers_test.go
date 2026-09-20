package agent

import (
	"io"
	"os"
	"testing"

	"github.com/PivotLLM/ClawEh/media"
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

// mustStore stores localPath in the media store or fails the test.
func mustStore(t *testing.T, store *media.FileMediaStore, localPath string, meta media.MediaMeta) string {
	t.Helper()
	ref, err := store.Store(localPath, meta, "test")
	if err != nil {
		t.Fatalf("Store(%s): %v", localPath, err)
	}
	return ref
}
