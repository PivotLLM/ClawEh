package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/PivotLLM/ctxengine/memory"
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

func TestHandleListSessions_ArchiveDB(t *testing.T) {
	configPath := setupTestEnv(t)

	dir := sessionsTestDir(t, configPath)
	seedSession(t, dir, webuiSessionPrefix+"history-db", "DB-backed session",
		providers.Message{Role: "user", Content: "Explain why the history API is empty after migration."},
		providers.Message{Role: "assistant", Content: "Because the API still reads only legacy JSON session files."},
		providers.Message{Role: "tool", Content: "ignored"},
	)

	mux := newSessionsMux(t, configPath)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "history-db" {
		t.Fatalf("items[0].ID = %q, want %q", items[0].ID, "history-db")
	}
	if items[0].MessageCount != 2 {
		t.Fatalf("items[0].MessageCount = %d, want 2", items[0].MessageCount)
	}
	if items[0].Title != "DB-backed session" {
		t.Fatalf("items[0].Title = %q, want %q", items[0].Title, "DB-backed session")
	}
	if items[0].Preview != "Explain why the history API is empty after migration." {
		t.Fatalf("items[0].Preview = %q", items[0].Preview)
	}
	if items[0].Created == "" || items[0].Updated == "" {
		t.Fatalf("items[0] timestamps empty: %+v", items[0])
	}
}

func TestHandleListSessions_IgnoresNonWebUISessions(t *testing.T) {
	configPath := setupTestEnv(t)

	dir := sessionsTestDir(t, configPath)
	seedSession(t, dir, "agent:main:telegram:direct:12345", "telegram chat",
		providers.Message{Role: "user", Content: "not for the webui"},
	)
	seedSession(t, dir, webuiSessionPrefix+"mine", "",
		providers.Message{Role: "user", Content: "mine"},
	)

	mux := newSessionsMux(t, configPath)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 || items[0].ID != "mine" {
		t.Fatalf("items = %+v, want only the webui session", items)
	}
}

func TestHandleListSessions_TitleUsesTrimmedSummary(t *testing.T) {
	configPath := setupTestEnv(t)

	dir := sessionsTestDir(t, configPath)
	seedSession(t, dir, webuiSessionPrefix+"summary-title",
		"  This summary is intentionally longer than sixty characters so it must be truncated in the history menu.  ",
		providers.Message{Role: "user", Content: "fallback preview"},
	)

	mux := newSessionsMux(t, configPath)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	expectedTitle := truncateRunes(
		"This summary is intentionally longer than sixty characters so it must be truncated in the history menu.",
		maxSessionTitleRunes,
	)
	if items[0].Title != expectedTitle {
		t.Fatalf("items[0].Title = %q", items[0].Title)
	}
	if items[0].Preview != "fallback preview" {
		t.Fatalf("items[0].Preview = %q, want %q", items[0].Preview, "fallback preview")
	}
}

func TestHandleGetSession_ArchiveDB(t *testing.T) {
	configPath := setupTestEnv(t)

	dir := sessionsTestDir(t, configPath)
	seedSession(t, dir, webuiSessionPrefix+"detail-db", "detail summary",
		providers.Message{Role: "user", Content: "first"},
		providers.Message{Role: "assistant", Content: "second"},
		providers.Message{Role: "tool", Content: "ignored"},
	)

	mux := newSessionsMux(t, configPath)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-db", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		ID       string `json:"id"`
		Summary  string `json:"summary"`
		Created  string `json:"created"`
		Updated  string `json:"updated"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.ID != "detail-db" {
		t.Fatalf("resp.ID = %q, want %q", resp.ID, "detail-db")
	}
	if resp.Summary != "detail summary" {
		t.Fatalf("resp.Summary = %q, want %q", resp.Summary, "detail summary")
	}
	if resp.Created == "" || resp.Updated == "" {
		t.Fatalf("timestamps empty: created=%q updated=%q", resp.Created, resp.Updated)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("len(resp.Messages) = %d, want 2", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "first" {
		t.Fatalf("first message = %#v, want user/first", resp.Messages[0])
	}
	if resp.Messages[1].Role != "assistant" || resp.Messages[1].Content != "second" {
		t.Fatalf("second message = %#v, want assistant/second", resp.Messages[1])
	}
}

func TestHandleGetSession_NotFound(t *testing.T) {
	configPath := setupTestEnv(t)
	sessionsTestDir(t, configPath)

	mux := newSessionsMux(t, configPath)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/does-not-exist", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestHandleDeleteSession_RemovesWholeDB(t *testing.T) {
	configPath := setupTestEnv(t)

	dir := sessionsTestDir(t, configPath)
	sessionKey := webuiSessionPrefix + "delete-db"
	seedSession(t, dir, sessionKey, "delete summary",
		providers.Message{Role: "user", Content: "delete me"},
	)
	dbPath := memory.ArchivePath(dir, sessionKey)
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected %s to exist before delete: %v", dbPath, err)
	}

	mux := newSessionsMux(t, configPath)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/delete-db", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err = %v", path, err)
		}
	}

	// Deleting again finds nothing.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/sessions/delete-db", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// A DB that holds a state row but no messages and no summary (a turn that
// started and stored nothing) must be filtered from the list and 404 on get.
func TestHandleSessions_FiltersEmptySessionDBs(t *testing.T) {
	configPath := setupTestEnv(t)

	dir := sessionsTestDir(t, configPath)
	sessionKey := webuiSessionPrefix + "empty-db"
	store, err := session.NewSQLiteStore(dir)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	if err := store.SetPendingTurn(sessionKey); err != nil {
		t.Fatalf("SetPendingTurn() error = %v", err)
	}
	if err := store.ClearPendingTurn(sessionKey); err != nil {
		t.Fatalf("ClearPendingTurn() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(memory.ArchivePath(dir, sessionKey)); err != nil {
		t.Fatalf("expected the session DB to exist: %v", err)
	}

	mux := newSessionsMux(t, configPath)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/sessions/empty-db", nil)
	mux.ServeHTTP(detailRec, detailReq)

	if detailRec.Code != http.StatusNotFound {
		t.Fatalf("detail status = %d, want %d, body=%s", detailRec.Code, http.StatusNotFound, detailRec.Body.String())
	}
}
