package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// AllowAnyAddress is the allowlist entry meaning "any client address, IPv4 or
// IPv6". Spelled the same way as the wildcards in allow_from and allow_origins.
//
// It exists because "0.0.0.0/0" is a natural thing to reach for and does NOT
// mean that: it is an IPv4 prefix, so an IPv6 client is still refused by it, and
// on a dual-stack host that reads as the allowlist simply not working. CIDRs are
// left meaning exactly what they say — widening 0.0.0.0/0 to cover IPv6 would
// fail open, which is the wrong direction to guess in — so "*" is the explicit
// way to say "everything".
const AllowAnyAddress = "*"

// Allowlist is a compiled set of client networks. It is immutable once built,
// which is what lets the gateway swap a freshly compiled one in on a config
// reload without recreating the listener (see internal/gateway.httpHost).
type Allowlist struct {
	allowAny bool
	nets     []*net.IPNet
}

// CompileAllowlist parses allowedCIDRs into an Allowlist, rejecting any entry
// that is neither a valid CIDR nor AllowAnyAddress.
//
// An empty allowedCIDRs compiles to LOOPBACK ONLY, not allow-all. This is
// deliberate: the handler behind this middleware is the unauthenticated
// WebUI/API, so the failure mode of the opposite reading — a config that omits
// the key silently exposing config and credentials to the whole network — is far
// worse than an install that has to be told who may connect.
func CompileAllowlist(allowedCIDRs []string) (*Allowlist, error) {
	list := &Allowlist{nets: make([]*net.IPNet, 0, len(allowedCIDRs))}
	for _, cidr := range allowedCIDRs {
		if strings.TrimSpace(cidr) == AllowAnyAddress {
			list.allowAny = true
			continue
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
		list.nets = append(list.nets, ipNet)
	}
	return list, nil
}

// Allows reports whether ip may be served. Loopback is always allowed, so local
// administration survives any allowlist. A nil Allowlist means loopback only,
// matching an empty CIDR list; a nil ip (an unparseable RemoteAddr) is refused.
func (a *Allowlist) Allows(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if a == nil {
		return false
	}
	if a.allowAny {
		return true
	}
	for _, ipNet := range a.nets {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// IPAllowlist gates next on the client's source address. The allowlist is read
// through current on every request rather than captured, so a config reload can
// change the policy on a live listener — the recovery path for an operator who
// has locked themselves out (`claw network`).
//
// It matches the TCP peer (RemoteAddr), so behind a reverse proxy the access
// control belongs at the proxy instead.
func IPAllowlist(current func() *Allowlist, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if current().Allows(clientIPFromRemoteAddr(r.RemoteAddr)) {
			next.ServeHTTP(w, r)
			return
		}
		rejectByPolicy(w, r)
	})
}

func clientIPFromRemoteAddr(remoteAddr string) net.IP {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	return net.ParseIP(host)
}

func rejectByPolicy(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"access denied by network policy"}`))
		return
	}
	http.Error(w, "Forbidden", http.StatusForbidden)
}
