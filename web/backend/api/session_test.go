package api

import (
	"net/http"
	"os"
	"testing"

	"github.com/PivotLLM/ctxengine/session"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
)

func sessionsTestDir(t *testing.T, configPath string) string {
	t.Helper()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	dirs := cfg.AgentSessionDirs()
	if len(dirs) == 0 {
		t.Fatal("AgentSessionDirs() returned empty slice")
	}
	dir := dirs[0]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	return dir
}

// seedSession writes messages and a summary for sessionKey through the real
// store, then closes it so the handlers read a quiescent DB the way they do
// in production (the gateway holds its own handle; the WebUI opens read-only).
func seedSession(t *testing.T, dir, sessionKey, summary string, msgs ...providers.Message) {
	t.Helper()

	store, err := session.NewSQLiteStore(dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	for _, msg := range msgs {
		if _, err = store.AddFullMessage(sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}
	if summary != "" {
		if err = store.SetSummary(sessionKey, summary); err != nil {
			t.Fatalf("SetSummary() error = %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func newSessionsMux(t *testing.T, configPath string) *http.ServeMux {
	t.Helper()
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}
