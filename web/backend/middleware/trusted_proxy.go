package middleware

import (
	"net/http"

	"github.com/PivotLLM/ClawEh/config"
)

// TrustedProxy attributes a request to the client behind a trusted reverse
// proxy (gateway.trusted_proxies): when the TCP peer is in the set read
// through current, r.RemoteAddr is replaced with the address from X-Real-IP
// (else the first X-Forwarded-For entry), so the IP allowlist, the login
// lockout, lockout_exempt, logs and the audit log all see the real client.
// From any other peer those headers are ignored. It must be the outermost
// handler. The set is read per request, so a config reload applies at once.
func TrustedProxy(current func() *config.TrustedProxySet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr := current().ClientAddr(r.RemoteAddr, r.Header.Get("X-Real-IP"), r.Header.Get("X-Forwarded-For"))
		if peer := clientIPFromRemoteAddr(r.RemoteAddr); peer == nil || peer.String() != addr {
			r.RemoteAddr = addr
		}
		next.ServeHTTP(w, r)
	})
}
