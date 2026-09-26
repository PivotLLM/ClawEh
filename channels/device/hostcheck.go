package device

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/PivotLLM/ClawEh/logger"
)

// hostAllowlist is the set of names the device listener answers to. It is the
// DNS-rebinding guard: a page that points an attacker's hostname at this
// machine sends that hostname as Host, and no legitimate client does.
type hostAllowlist map[string]bool

// newHostAllowlist accepts localhost, the bind host, the host of each URL (the
// device external_url and gateway.external_url) and any extra names (future
// TLS SANs). Names are compared case-insensitively; blanks are skipped.
func newHostAllowlist(bindHost string, urls []string, extra []string) hostAllowlist {
	al := hostAllowlist{"localhost": true}
	add := func(h string) {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			al[h] = true
		}
	}
	add(bindHost)
	for _, raw := range urls {
		if u, err := url.Parse(strings.TrimSpace(raw)); err == nil {
			add(u.Hostname())
		}
	}
	for _, h := range extra {
		add(h)
	}
	return al
}

// allows reports whether a Host header (port optional) names this listener. An
// IP literal is always allowed: it cannot be rebound, and the pairing QR
// advertises the LAN IP when no external_url is configured.
func (al hostAllowlist) allows(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if net.ParseIP(host) != nil {
		return true
	}
	return al[strings.ToLower(host)]
}

// hostCheckHandler answers 421 Misdirected Request when the Host header is not
// on the allowlist, before the request reaches the WebSocket upgrade.
func hostCheckHandler(allowed hostAllowlist, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed.allows(r.Host) {
			logger.WarnCF("device", "request rejected: Host not allowed", map[string]any{
				"host": r.Host, "remoteIp": r.RemoteAddr,
			})
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
