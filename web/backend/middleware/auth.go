package middleware

import (
	"net/http"
	"strings"

	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/logger"
)

// AuthExemptPaths are the routes every install serves without a login
// session, in ServeMux pattern syntax. Each carries its own gate or gives
// nothing away:
//
//   - /health, /ready — liveness and readiness probes.
//   - /ping, /api/v1/ — the MCPFusion OAuth API that claw-auth drives; it
//     authenticates its own requests.
//   - POST /api/message/{token} — the token-in-path message API.
//   - the auth endpoints themselves, or nobody could log in.
var AuthExemptPaths = []string{
	"/health",
	"/ready",
	"/ping",
	"/api/v1/",
	"POST /api/message/{token}",
	"POST /api/auth/login",
	"POST /api/auth/logout",
	"GET /api/auth/status",
}

// authExempt is AuthExemptPaths compiled into a ServeMux, so matching agrees
// with the real mux on prefixes, methods and path cleaning.
var authExempt = func() *http.ServeMux {
	mux := http.NewServeMux()
	marker := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, p := range AuthExemptPaths {
		mux.Handle(p, marker)
	}
	return mux
}()

// IsAuthExempt reports whether r is on one of AuthExemptPaths and so bypasses
// the login check.
func IsAuthExempt(r *http.Request) bool {
	_, pattern := authExempt.Handler(r)
	return pattern != ""
}

// Auth requires a login session on every request that could act on or read
// from the gateway: /api/*, the WebUI WebSocket under /webui/, the manual
// under /manual/, and any non-GET request. Exempt routes pass untouched. GET/HEAD requests elsewhere
// (the SPA shell and its assets, which hold no secrets) pass too — that is how
// the login page reaches the browser; the frontend's own gate then keeps an
// unauthenticated visitor on it.
//
// Without an admin account (no usable credentials file) every protected
// request is refused with 401 and the hint to run `claw admin`. The store is
// read per request through a current-value func; a nil store fails closed.
func Auth(store func() *AuthStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsAuthExempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		s := store()
		if s != nil {
			if user, ok := s.Authenticate(r); ok {
				// Handlers that write audit rows read the actor from the context.
				next.ServeHTTP(w, r.WithContext(audit.WithActor(r.Context(), user)))
				return
			}
		}
		if !authProtected(r) || channelTokenPresented(r) {
			next.ServeHTTP(w, r)
			return
		}
		configured := s != nil && s.Configured()
		logger.DebugCF("auth", "Unauthenticated request refused", map[string]any{
			"method": r.Method, "path": r.URL.Path, "remote": r.RemoteAddr, "configured": configured,
		})
		WriteUnauthorized(w, r, configured)
	})
}

// channelTokenPresented reports whether r is a WebUI-channel request from a
// non-browser client carrying the channel token (`Authorization: Bearer` or
// the `claw-token` WebSocket subprotocol). Those requests pass to the channel,
// which validates the token itself; without a token the login applies.
func channelTokenPresented(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/webui/") {
		return false
	}
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return true
	}
	for p := range strings.SplitSeq(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		if strings.HasPrefix(strings.TrimSpace(p), "claw-token") {
			return true
		}
	}
	return false
}

// authProtected reports whether an unauthenticated r must be refused rather
// than passed through as static UI.
func authProtected(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true
	}
	p := r.URL.Path
	return p == "/api" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/webui/") ||
		p == "/manual" || strings.HasPrefix(p, "/manual/")
}

// WriteUnauthorized writes the 401 the WebUI client understands: a JSON body
// under /api/ (with the `claw admin` hint when no account exists), plain text
// elsewhere.
func WriteUnauthorized(w http.ResponseWriter, r *http.Request, configured bool) {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	body := `{"error":"authentication required"}`
	if !configured {
		body = `{"error":"no admin account","hint":"run: claw admin"}`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	if _, err := w.Write([]byte(body)); err != nil {
		logger.DebugCF("http", "response write failed", map[string]any{"error": err.Error()})
	}
}
