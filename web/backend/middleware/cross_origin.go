package middleware

import (
	"fmt"
	"net/http"

	"github.com/PivotLLM/ClawEh/logger"
)

// strictCrossOrigin is what a nil policy means: no trusted origins and no
// bypasses, so the state before the first SetCrossOrigin fails closed.
var strictCrossOrigin = http.NewCrossOriginProtection()

// NewCrossOriginProtection builds the cross-site request policy the gateway
// swaps in on boot and reload. trustedOrigins ("scheme://host[:port]") are
// allowed to make unsafe cross-origin requests; bypassPatterns (ServeMux
// syntax) are routes that carry their own authentication — a signed channel
// webhook, a token-in-path API — and so may be posted to from anywhere. An
// invalid pattern panics, exactly as registering it on the mux would.
func NewCrossOriginProtection(trustedOrigins, bypassPatterns []string) (*http.CrossOriginProtection, error) {
	c := http.NewCrossOriginProtection()
	for _, origin := range trustedOrigins {
		if err := c.AddTrustedOrigin(origin); err != nil {
			return nil, fmt.Errorf("invalid trusted origin %q: %w", origin, err)
		}
	}
	for _, pattern := range bypassPatterns {
		c.AddInsecureBypassPattern(pattern)
	}
	return c, nil
}

// CrossOrigin rejects unsafe (non GET/HEAD/OPTIONS) requests that a browser
// marks as coming from another site, using Sec-Fetch-Site or, failing that,
// Origin against Host. Requests with neither header are not from a browser and
// pass. This closes the no-preflight POST hole: without it any web page could
// drive /api/* on a listener the visitor's machine can reach.
//
// The policy is read through current on every request so a config reload can
// change the trusted origin on a live listener.
func CrossOrigin(current func() *http.CrossOriginProtection, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policy := current()
		if policy == nil {
			policy = strictCrossOrigin
		}
		if err := policy.Check(r); err != nil {
			logger.WarnCF("http", "Cross-origin request rejected", map[string]any{
				"origin":         r.Header.Get("Origin"),
				"sec_fetch_site": r.Header.Get("Sec-Fetch-Site"),
				"method":         r.Method,
				"path":           r.URL.Path,
				"remote":         r.RemoteAddr,
			})
			reject(w, r, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		next.ServeHTTP(w, r)
	})
}
