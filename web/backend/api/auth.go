// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"fmt"
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
// attempts count toward the per-address and per-username lockout
// (middleware.LoginLimiter); an attempt while either is locked gets 429 with
// Retry-After before the password is checked, and restarts the lock. A change
// to the credentials file (`claw admin`) clears every lock first.
func (h *Handler) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	store := h.authStore()
	if store == nil {
		middleware.WriteUnauthorized(w, r, false)
		return
	}
	if h.loginLimiter.SyncCredentials(store.Refresh()) {
		logger.InfoCF("auth", "Credentials file changed; login locks cleared", nil)
	}
	if !store.Configured() {
		middleware.WriteUnauthorized(w, r, false)
		return
	}
	ip := clientIP(r)

	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxLoginBody)).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if wait := h.loginLimiter.Attempt(ip, in.Username); wait > 0 {
		logger.WarnCF("auth", "Login refused: locked", map[string]any{"username": in.Username, "ip": ip})
		audit.Auth("locked", in.Username, ip, audit.OutcomeError)
		writeLoginLocked(w, wait)
		return
	}

	id, ok := store.Login(in.Username, in.Password)
	if !ok {
		started := h.loginLimiter.Failure(ip, in.Username)
		logger.WarnCF("auth", "Login failed", map[string]any{"username": in.Username, "ip": ip})
		audit.Auth("login", in.Username, ip, audit.OutcomeError)
		for _, lock := range started {
			h.alertLoginLockout(lock, ip, in.Username)
		}
		writeJSONError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	h.loginLimiter.Success(ip, in.Username)
	middleware.SetSessionCookie(w, r, id)
	logger.InfoCF("auth", "Login", map[string]any{"username": in.Username, "ip": ip})
	audit.Auth("login", in.Username, ip, audit.OutcomeOK)
	w.WriteHeader(http.StatusNoContent)
}

// alertLoginLockout logs, audits and alerts a lockout that has just started.
func (h *Handler) alertLoginLockout(lock middleware.LockStart, ip, username string) {
	kind := "address"
	a := alerter.Alert{
		Priority:    alerter.Normal,
		Title:       "WebUI login address locked out",
		Description: fmt.Sprintf("%s was locked out after %d failed logins in ten minutes; every further attempt from it restarts the %s lock", ip, middleware.LoginIPFailures, lock.For),
		Details:     "last username tried: " + username,
		EventID:     "auth-lockout-ip",
	}
	if lock.Account {
		kind = "account"
		a = alerter.Alert{
			Priority:    alerter.Normal,
			Title:       "WebUI login account locked out",
			Description: fmt.Sprintf("username %q was locked out after %d failed logins in ten minutes; every further attempt for it restarts the %s lock", username, middleware.LoginUserFailures, lock.For),
			Details:     "last client address: " + ip,
			EventID:     "auth-lockout-account",
		}
	}
	logger.WarnCF("auth", "Login lockout", map[string]any{"kind": kind, "username": username, "ip": ip, "lock": lock.For.String()})
	audit.Auth("lockout", username, ip, audit.OutcomeError)
	h.alerterRef().Send(a)
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
