package middleware

import (
	"net/http"
	"strings"
)

// SecurityHeaders sets the browser hardening headers on every response: the
// page may not be framed (clickjacking), responses are not content-sniffed, and
// no Referer leaves the WebUI. /api/* responses are additionally marked
// no-store so config and token JSON never lands in a browser or proxy cache.
// Headers are set before the handler runs, so a handler that needs to differ
// (the SPA's asset Cache-Control) still wins.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
