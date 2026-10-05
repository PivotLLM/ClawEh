package api

import (
	"net/http"
	"strings"

	"github.com/PivotLLM/ClawEh/internal/sessions"
	"github.com/PivotLLM/ClawEh/logger"
)

// registerSessionRoutes binds the session erase endpoint to the ServeMux.
func (h *Handler) registerSessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("DELETE /api/sessions", h.handleEraseSessions)
}

// SetSessionReleaser wires the running agent loop's session release into the
// handler so DELETE /api/sessions can close a session's live handles before
// deleting its archive; the release fails while a turn is in flight.
func (h *Handler) SetSessionReleaser(fn func(sessionKey string) error) {
	h.reloadMu.Lock()
	h.sessionReleaser = fn
	h.reloadMu.Unlock()
}

func (h *Handler) sessionReleaserRef() func(sessionKey string) error {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	return h.sessionReleaser
}

// sessionsDirs returns the sessions directories for every configured agent.
// Multiple agents may have distinct workspaces; the WebUI must search all of
// them to enumerate or locate sessions.
func (h *Handler) sessionsDirs() ([]string, error) {
	cfg, err := h.currentConfig()
	if err != nil {
		return nil, err
	}
	return cfg.AgentSessionDirs(), nil
}

// handleEraseSessions deletes every session archive belonging to one sender
// on one channel, across agents, and answers with the erase report: the keys
// removed, any left in place (a turn in flight), the shared main session
// that was kept unless all=true, and the cogmem note.
//
//	DELETE /api/sessions?channel=<ch>&chat_id=<id>[&all=true]
func (h *Handler) handleEraseSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := sessions.EraseRequest{
		Channel: q.Get("channel"),
		ChatID:  q.Get("chat_id"),
		All:     q.Get("all") == "true",
	}
	if strings.TrimSpace(req.Channel) == "" || strings.TrimSpace(req.ChatID) == "" {
		http.Error(w, "channel and chat_id are required", http.StatusBadRequest)
		return
	}
	cfg, err := h.currentConfig()
	if err != nil {
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}
	rep, err := sessions.Erase(cfg, req, h.sessionReleaserRef())
	if err != nil {
		logger.WarnCF("api", "session erase failed", map[string]any{
			"channel": req.Channel, "chat_id": req.ChatID, "erased": len(rep.Erased), "error": err.Error(),
		})
		http.Error(w, "failed to erase sessions", http.StatusInternalServerError)
		return
	}
	logger.InfoCF("api", "sessions erased for sender", map[string]any{
		"channel": req.Channel, "chat_id": req.ChatID, "erased": rep.Erased, "skipped": len(rep.Skipped), "all": req.All,
	})
	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, rep)
}
