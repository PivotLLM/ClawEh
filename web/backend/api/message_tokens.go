// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/msgtoken"
	"github.com/PivotLLM/ClawEh/pkg/routing"
)

// messageTokenLoop is the slice of the running AgentLoop the message-token API
// needs. It is injected via SetMessageTokenLoop so this package stays decoupled
// from pkg/agent, and — critically — so the API mutates the SAME named-token
// store instance the gateway validates against, making a mint/revoke effective
// with no reload.
type messageTokenLoop interface {
	ListMessageTokens(agentID string) []msgtoken.NamedToken
	CreateMessageToken(agentID, name string) (msgtoken.NamedToken, error)
	DeleteMessageToken(agentID, id string) bool
}

// SetMessageTokenLoop wires the live AgentLoop into the handler so the
// message-token endpoints operate on the gateway's in-memory store.
func (h *Handler) SetMessageTokenLoop(loop messageTokenLoop) {
	h.reloadMu.Lock()
	h.msgTokenLoop = loop
	h.reloadMu.Unlock()
}

func (h *Handler) messageTokenLoopRef() messageTokenLoop {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	return h.msgTokenLoop
}

// registerMessageTokenRoutes wires the named message-API token management endpoints.
//
// SECURITY: these endpoints expose plaintext long-lived tokens that grant the
// ability to inject messages into an agent. They inherit the same operator-auth
// posture as the rest of /api/* — do not expose this API on an untrusted network.
func (h *Handler) registerMessageTokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents/{id}/message-tokens", h.handleListMessageTokens)
	mux.HandleFunc("POST /api/agents/{id}/message-tokens", h.handleCreateMessageToken)
	mux.HandleFunc("DELETE /api/agents/{id}/message-tokens/{tokenId}", h.handleDeleteMessageToken)
}

// messageTokenView is the JSON shape returned for a single named token. The
// plaintext token is included on purpose — the operator must be able to copy it
// into the external app.
type messageTokenView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Token       string `json:"token"`
	CreatedAtMS int64  `json:"created_at_ms"`
}

func toMessageTokenView(t msgtoken.NamedToken) messageTokenView {
	return messageTokenView{ID: t.ID, Name: t.Name, Token: t.Token, CreatedAtMS: t.CreatedAtMS}
}

// agentExists reports whether the given (already-normalized) agent id is present
// in the config's agent list.
func agentExists(cfg *config.Config, agentID string) bool {
	for i := range cfg.Agents.List {
		if routing.NormalizeAgentID(cfg.Agents.List[i].ID) == agentID {
			return true
		}
	}
	return false
}

// messageEndpointBase builds the absolute base URL a token is appended to, ending
// in "/api/message/". The message route lives on the same shared HTTP port as the
// WebUI, so the request's own Host header is the authoritative reachable address.
func messageEndpointBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/api/message/"
}

func (h *Handler) resolveAgentID(w http.ResponseWriter, r *http.Request) (string, bool) {
	agentID := routing.NormalizeAgentID(r.PathValue("id"))
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config load failed"})
		return "", false
	}
	if !agentExists(cfg, agentID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return "", false
	}
	return agentID, true
}

func (h *Handler) handleListMessageTokens(w http.ResponseWriter, r *http.Request) {
	agentID, ok := h.resolveAgentID(w, r)
	if !ok {
		return
	}
	loop := h.messageTokenLoopRef()
	views := []messageTokenView{}
	if loop != nil {
		for _, t := range loop.ListMessageTokens(agentID) {
			views = append(views, toMessageTokenView(t))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tokens":        views,
		"endpoint_base": messageEndpointBase(r),
	})
}

func (h *Handler) handleCreateMessageToken(w http.ResponseWriter, r *http.Request) {
	agentID, ok := h.resolveAgentID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	loop := h.messageTokenLoopRef()
	if loop == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "message tokens unavailable"})
		return
	}
	tok, err := loop.CreateMessageToken(agentID, strings.TrimSpace(body.Name))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create failed"})
		return
	}
	writeJSON(w, http.StatusCreated, toMessageTokenView(tok))
}

func (h *Handler) handleDeleteMessageToken(w http.ResponseWriter, r *http.Request) {
	agentID, ok := h.resolveAgentID(w, r)
	if !ok {
		return
	}
	tokenID := r.PathValue("tokenId")
	loop := h.messageTokenLoopRef()
	if loop == nil || !loop.DeleteMessageToken(agentID, tokenID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "token not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
