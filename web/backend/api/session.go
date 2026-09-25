package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PivotLLM/ctxengine/memory"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/utils"
)

// registerSessionRoutes binds session list and detail endpoints to the ServeMux.
func (h *Handler) registerSessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions", h.handleListSessions)
	mux.HandleFunc("GET /api/sessions/{id}", h.handleGetSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", h.handleDeleteSession)
}

// sessionFile is the view of one session the handlers build from its archive
// DB: the live window plus the session state.
type sessionFile struct {
	Key      string
	Messages []providers.Message
	Summary  string
	Created  time.Time
	Updated  time.Time
}

// sessionListItem is a lightweight summary returned by GET /api/sessions.
type sessionListItem struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Preview      string `json:"preview"`
	MessageCount int    `json:"message_count"`
	Created      string `json:"created"`
	Updated      string `json:"updated"`
}

// webuiSessionPrefix is the key prefix used by the gateway's routing for WebUI
// channel sessions. The full key format is:
//
//	agent:main:webui:direct:webui:<session-uuid>
//
// The sanitized filename replaces ':' with '_', so on disk the session's store
// becomes:
//
//	agent_main_webui_direct_webui_<session-uuid>.archive.db
const (
	webuiSessionPrefix          = "agent:main:webui:direct:webui:"
	sanitizedWebuiSessionPrefix = "agent_main_webui_direct_webui_"
	sessionDBSuffix             = ".archive.db"
	maxSessionTitleRunes        = 60
)

func extractWebUISessionIDFromSanitizedKey(key string) (string, bool) {
	if after, ok := strings.CutPrefix(key, sanitizedWebuiSessionPrefix); ok {
		return after, true
	}
	return "", false
}

// readSessionDB opens one session's archive DB read-only and returns its
// window and state. A missing DB is reported as os.ErrNotExist.
func readSessionDB(path string) (sessionFile, error) {
	info, err := os.Stat(path) //nolint:gosec // callers pass memory.ArchivePath output (sanitized key) or a ReadDir entry
	if err != nil {
		return sessionFile{}, err
	}

	db, err := memory.OpenReadOnly(path)
	if err != nil {
		return sessionFile{}, err
	}
	defer utils.CloseQuietly(db)

	window, err := db.Window()
	if err != nil {
		return sessionFile{}, err
	}
	state, err := db.State()
	if err != nil {
		return sessionFile{}, err
	}

	messages := make([]providers.Message, 0, len(window))
	for _, stored := range window {
		messages = append(messages, stored.Message)
	}

	created, updated := state.CreatedAt, state.UpdatedAt
	if created.IsZero() {
		created = info.ModTime()
	}
	if updated.IsZero() {
		updated = info.ModTime()
	}

	return sessionFile{
		Key:      state.Key,
		Messages: messages,
		Summary:  state.Summary,
		Created:  created,
		Updated:  updated,
	}, nil
}

// readSession loads the WebUI session with the given id from dir.
func readSession(dir, sessionID string) (sessionFile, error) {
	return readSessionDB(memory.ArchivePath(dir, webuiSessionPrefix+sessionID))
}

func buildSessionListItem(sessionID string, sess sessionFile) sessionListItem {
	preview := ""
	for _, msg := range sess.Messages {
		if msg.Role == "user" && strings.TrimSpace(msg.Content) != "" {
			preview = msg.Content
			break
		}
	}
	title := strings.TrimSpace(sess.Summary)
	if title == "" {
		title = preview
	}

	title = truncateRunes(title, maxSessionTitleRunes)
	preview = truncateRunes(preview, maxSessionTitleRunes)

	if preview == "" {
		preview = "(empty)"
	}
	if title == "" {
		title = preview
	}

	validMessageCount := 0
	for _, msg := range sess.Messages {
		if (msg.Role == "user" || msg.Role == "assistant") && strings.TrimSpace(msg.Content) != "" {
			validMessageCount++
		}
	}

	return sessionListItem{
		ID:           sessionID,
		Title:        title,
		Preview:      preview,
		MessageCount: validMessageCount,
		Created:      sess.Created.Format(time.RFC3339),
		Updated:      sess.Updated.Format(time.RFC3339),
	}
}

func isEmptySession(sess sessionFile) bool {
	return len(sess.Messages) == 0 && strings.TrimSpace(sess.Summary) == ""
}

func truncateRunes(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= maxLen {
		return string(runes)
	}
	return string(runes[:maxLen]) + "..."
}

// sessionsDirs returns the sessions directories for every configured agent.
// Multiple agents may have distinct workspaces; the WebUI must search all of
// them to enumerate or locate sessions. The defaults workspace is always
// included as a fallback for agents removed from config but with files on disk.
func (h *Handler) sessionsDirs() ([]string, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return nil, err
	}
	return cfg.AgentSessionDirs(), nil
}

// handleListSessions returns a list of WebUI session summaries.
//
//	GET /api/sessions
func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	dirs, err := h.sessionsDirs()
	if err != nil {
		http.Error(w, "failed to resolve sessions directory", http.StatusInternalServerError)
		return
	}

	items := []sessionListItem{}
	seen := make(map[string]struct{})

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // directory doesn't exist yet — skip
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			name := entry.Name()
			if !strings.HasSuffix(name, sessionDBSuffix) {
				continue // -wal/-shm sidecars and anything else
			}
			sessionID, ok := extractWebUISessionIDFromSanitizedKey(strings.TrimSuffix(name, sessionDBSuffix))
			if !ok {
				continue
			}
			if _, exists := seen[sessionID]; exists {
				continue
			}

			sess, loadErr := readSessionDB(filepath.Join(dir, name))
			if loadErr != nil || isEmptySession(sess) {
				continue
			}

			seen[sessionID] = struct{}{}
			items = append(items, buildSessionListItem(sessionID, sess))
		}
	}

	// Sort by updated descending (most recent first)
	sort.Slice(items, func(i, j int) bool {
		return items[i].Updated > items[j].Updated
	})

	// Pagination parameters
	offsetStr := r.URL.Query().Get("offset")
	limitStr := r.URL.Query().Get("limit")

	offset := 0
	limit := 20 // Default limit

	if val, err := strconv.Atoi(offsetStr); err == nil && val >= 0 {
		offset = val
	}
	if val, err := strconv.Atoi(limitStr); err == nil && val > 0 {
		limit = val
	}

	totalItems := len(items)

	end := offset + limit
	if offset >= totalItems {
		items = []sessionListItem{} // Out of bounds, return empty
	} else {
		if end > totalItems {
			end = totalItems
		}
		items = items[offset:end]
	}

	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, items)
}

// handleGetSession returns the full message history for a specific session.
//
//	GET /api/sessions/{id}
func (h *Handler) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}

	dirs, err := h.sessionsDirs()
	if err != nil {
		http.Error(w, "failed to resolve sessions directory", http.StatusInternalServerError)
		return
	}

	var sess sessionFile
	var found bool
	for _, dir := range dirs {
		s, e := readSession(dir, sessionID)
		if e == nil && !isEmptySession(s) {
			sess, found = s, true
			break
		}
	}
	if !found {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Convert to a simpler format for the frontend
	type chatMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	messages := make([]chatMessage, 0, len(sess.Messages))
	for _, msg := range sess.Messages {
		// Only include user and assistant messages that have actual content
		if (msg.Role == "user" || msg.Role == "assistant") && strings.TrimSpace(msg.Content) != "" {
			messages = append(messages, chatMessage{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]any{
		"id":       sessionID,
		"messages": messages,
		"summary":  sess.Summary,
		"created":  sess.Created.Format(time.RFC3339),
		"updated":  sess.Updated.Format(time.RFC3339),
	})
}

// handleDeleteSession deletes a specific session: its whole store, archive
// included.
//
//	DELETE /api/sessions/{id}
func (h *Handler) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}

	dirs, err := h.sessionsDirs()
	if err != nil {
		http.Error(w, "failed to resolve sessions directory", http.StatusInternalServerError)
		return
	}

	key := webuiSessionPrefix + sessionID
	removed := false
	for _, dir := range dirs {
		if _, err := os.Stat(memory.ArchivePath(dir, key)); err != nil { //nolint:gosec // memory.ArchivePath strips path separators from the key, which also carries a fixed prefix
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			http.Error(w, "failed to delete session", http.StatusInternalServerError)
			return
		}
		if err := memory.DeleteSession(dir, key); err != nil {
			http.Error(w, "failed to delete session", http.StatusInternalServerError)
			return
		}
		removed = true
	}

	if !removed {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
