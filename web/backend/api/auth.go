// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/web/backend/middleware"
)

// maxLoginBody bounds the login request body; a username and a password fit
// in far less.
const maxLoginBody = 4 << 10

// SetAuth wires the gateway's session store into the auth endpoints. Until it
// is set, login reports "no admin account".
func (h *Handler) SetAuth(store *middleware.AuthStore) {
	h.reloadMu.Lock()
	h.auth = store
	h.reloadMu.Unlock()
}

func (h *Handler) authStore() *middleware.AuthStore {
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	return h.auth
}

// registerAuthRoutes binds the login endpoints. All three are exempt from the
// auth middleware (middleware.AuthExemptPaths) — they are how a session is
// obtained, checked and ended.
func (h *Handler) registerAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", h.handleAuthLogin)
	mux.HandleFunc("POST /api/auth/logout", h.handleAuthLogout)
	mux.HandleFunc("GET /api/auth/status", h.handleAuthStatus)
}

// handleAuthStatus reports whether an admin account exists and whether the
// caller holds a session. The frontend gate reads it on every navigation.
//
//	GET /api/auth/status → {"configured":bool,"authenticated":bool,"username":"..."}
func (h *Handler) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"configured": false, "authenticated": false, "username": ""}
	if s := h.authStore(); s != nil {
		out["configured"] = s.Configured()
		if user, ok := s.Authenticate(r); ok {
			out["authenticated"] = true
			out["username"] = user
		}
	}
	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, out)
}

// handleAuthLogin verifies a username and password and opens a session.
//
//	POST /api/auth/login {"username":"...","password":"..."} → 204 + Set-Cookie
//
// Every failure — wrong username, wrong password — is the same 401 with the
// same message, so the response does not confirm which half was right. Failed
// attempts count toward the per-address and global backoff; a locked address
// gets 429 with Retry-After before the password is even checked.
func (h *Handler) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	store := h.authStore()
	if store == nil || !store.Configured() {
		middleware.WriteUnauthorized(w, r, false)
		return
	}
	ip := clientIP(r)

	if wait := h.loginLimiter.Check(ip); wait > 0 {
		writeLoginLocked(w, wait)
		return
	}

	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxLoginBody)).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	id, ok := store.Login(in.Username, in.Password)
	if !ok {
		locked, first := h.loginLimiter.Failure(ip)
		logger.WarnCF("auth", "Login failed", map[string]any{"username": in.Username, "ip": ip})
		audit.Auth("login", in.Username, ip, audit.OutcomeError)
		if locked > 0 {
			logger.WarnCF("auth", "Login lockout", map[string]any{"username": in.Username, "ip": ip, "lock": locked.String()})
			audit.Auth("lockout", in.Username, ip, audit.OutcomeError)
			if first {
				h.alerterRef().Send(alerter.Alert{
					Title:       "WebUI login locked out",
					Description: ip + " was locked out after repeated failed logins; the lock doubles on each repeat, up to an hour",
					Details:     "last username tried: " + in.Username,
					EventID:     "auth-lockout",
				})
			}
		}
		writeJSONError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	h.loginLimiter.Success(ip)
	middleware.SetSessionCookie(w, r, id)
	logger.InfoCF("auth", "Login", map[string]any{"username": in.Username, "ip": ip})
	audit.Auth("login", in.Username, ip, audit.OutcomeOK)
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthLogout ends the caller's session and clears the cookie.
//
//	POST /api/auth/logout → 204
func (h *Handler) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if store := h.authStore(); store != nil {
		for _, name := range []string{middleware.SessionCookieSecure, middleware.SessionCookieInsecure} {
			if c, err := r.Cookie(name); err == nil {
				if user, ok := store.Validate(c.Value); ok {
					ip := clientIP(r)
					logger.InfoCF("auth", "Logout", map[string]any{"username": user, "ip": ip})
					audit.Auth("logout", user, ip, audit.OutcomeOK)
				}
				store.Logout(c.Value)
			}
		}
	}
	middleware.ClearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func writeLoginLocked(w http.ResponseWriter, wait time.Duration) {
	secs := max(int(math.Ceil(wait.Seconds())), 1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	encodeJSON(w, map[string]any{"error": "too many failed logins", "retry_after": secs})
}
