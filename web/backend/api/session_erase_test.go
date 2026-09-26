package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/PivotLLM/ctxengine/memory"

	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

// DELETE /api/sessions?channel=&chat_id= removes the sender's sessions and
// leaves another sender's, and reports the keys it removed.
func TestHandleEraseSessions_RemovesSenderOnly(t *testing.T) {
	configPath := setupTestEnv(t)
	dir := sessionsTestDir(t, configPath)
	const mine, theirs = "agent:main:telegram:direct:555", "agent:main:telegram:direct:777"
	seedSession(t, dir, mine, "", providers.Message{Role: "user", Content: "erase me"})
	seedSession(t, dir, theirs, "", providers.Message{Role: "user", Content: "keep me"})

	released := []string{}
	h := NewHandler(configPath)
	h.SetSessionReleaser(func(key string) error {
		released = append(released, key)
		return nil
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/sessions?channel=telegram&chat_id=555", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var rep struct {
		Erased []string `json:"erased"`
		Shared string   `json:"shared_session"`
		Cogmem string   `json:"cogmem"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rep.Erased) != 1 || rep.Erased[0] != mine {
		t.Fatalf("erased = %v, want [%s]", rep.Erased, mine)
	}
	if rep.Cogmem == "" {
		t.Fatal("cogmem note missing from the report")
	}
	if len(released) != 1 || released[0] != mine {
		t.Fatalf("releaser called with %v, want [%s]", released, mine)
	}
	if _, err := os.Stat(memory.ArchivePath(dir, mine)); !os.IsNotExist(err) {
		t.Fatalf("erased session still on disk: %v", err)
	}
	if _, err := os.Stat(memory.ArchivePath(dir, theirs)); err != nil {
		t.Fatalf("other sender's session removed: %v", err)
	}
}

func TestHandleEraseSessions_RequiresChannelAndChatID(t *testing.T) {
	configPath := setupTestEnv(t)
	mux := newSessionsMux(t, configPath)
	for _, path := range []string{"/api/sessions", "/api/sessions?channel=telegram", "/api/sessions?chat_id=1"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("DELETE %s = %d, want 400", path, rec.Code)
		}
	}
}

// The session endpoints must never join the auth-exempt list: erasing a
// sender's history, like reading it, requires a login.
func TestSessionRoutes_RequireLogin(t *testing.T) {
	exempt := middleware.CompileAuthExempt()
	for _, tc := range []struct{ method, path string }{
		{http.MethodDelete, "/api/sessions?channel=telegram&chat_id=555"},
		{http.MethodDelete, "/api/sessions/abc"},
		{http.MethodGet, "/api/sessions"},
		{http.MethodGet, "/api/sessions/abc"},
	} {
		if exempt.Matches(httptest.NewRequest(tc.method, tc.path, nil)) {
			t.Errorf("%s %s is exempt from authentication", tc.method, tc.path)
		}
	}
}
