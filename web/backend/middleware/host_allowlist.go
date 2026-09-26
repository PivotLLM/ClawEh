package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/PivotLLM/ClawEh/logger"
)

// HostAllowlist is the set of names a request's Host header may carry. It is
// immutable once built, so the gateway can swap a fresh one in on a config
// reload (external_url may change) without recreating the listener.
//
// It exists to stop DNS rebinding: a page on an attacker's origin re-points its
// own hostname at 127.0.0.1 after the browser has loaded it, and from then on
// same-origin fetches reach this listener from the always-allowed loopback peer.
// The Host header still names the attacker's domain, which is what this rejects.
type HostAllowlist struct {
	hosts map[string]struct{}
}

// loopbackHosts are always allowed, whatever the caller supplies.
var loopbackHosts = []string{"localhost", "127.0.0.1", "::1"}

// loopbackOnly is what a nil *HostAllowlist means: the state before the first
// SetAllowedHosts fails closed to loopback names, like the IP allowlist.
var loopbackOnly = CompileHostAllowlist(nil)

// CompileHostAllowlist builds a HostAllowlist from names plus the loopback
// names. Entries are normalised the way request Hosts are (lower-cased, port and
// IPv6 brackets stripped), so "Claw.Example.COM:8443" and "[::1]:18790" match
// their bare forms. Empty entries are ignored.
func CompileHostAllowlist(names []string) *HostAllowlist {
	list := &HostAllowlist{hosts: make(map[string]struct{}, len(names)+len(loopbackHosts))}
	for _, group := range [][]string{loopbackHosts, names} {
		for _, name := range group {
			if host := normalizeHost(name); host != "" {
				list.hosts[host] = struct{}{}
			}
		}
	}
	return list
}

// Allows reports whether a request carrying hostHeader may be served. A nil
// HostAllowlist allows loopback names only, matching an empty name list.
func (a *HostAllowlist) Allows(hostHeader string) bool {
	if a == nil {
		a = loopbackOnly
	}
	host := normalizeHost(hostHeader)
	if host == "" {
		return false
	}
	_, ok := a.hosts[host]
	return ok
}

// normalizeHost lower-cases and strips the port and IPv6 brackets from a Host
// header or allowlist entry.
func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

// HostCheck rejects with 421 Misdirected Request any request whose Host is not
// in the current allowlist. The allowlist is read through current on every
// request so a config reload can change it on a live listener.
func HostCheck(current func() *HostAllowlist, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if current().Allows(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		logger.WarnCF("http", "Request for a host this gateway does not serve", map[string]any{
			"host":   r.Host,
			"remote": r.RemoteAddr,
			"path":   r.URL.Path,
		})
		reject(w, r, http.StatusMisdirectedRequest, "host not served by this gateway")
	})
}
