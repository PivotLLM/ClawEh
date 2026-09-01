package middleware

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// IPAllowlist restricts access to requests from configured CIDR ranges.
// Loopback addresses are always allowed for local administration.
// Empty CIDR list means no restriction.
// IPAllowlist gates next on the client's source address. Loopback is always
// allowed; every other address must fall inside one of allowedCIDRs.
//
// An empty allowedCIDRs means LOOPBACK ONLY, not allow-all. This is deliberate:
// the handler behind it is the unauthenticated WebUI/API, so the failure mode of
// the opposite reading — a config that omits the key silently exposing config
// and credentials to the whole network — is far worse than an install that has
// to be told who may connect. Pass []string{"0.0.0.0/0"} (and "::/0" for IPv6)
// to allow any address.
func IPAllowlist(allowedCIDRs []string, next http.Handler) (http.Handler, error) {
	nets := make([]*net.IPNet, 0, len(allowedCIDRs))
	for _, cidr := range allowedCIDRs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
		nets = append(nets, ipNet)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIPFromRemoteAddr(r.RemoteAddr)
		if ip == nil {
			rejectByPolicy(w, r)
			return
		}
		if ip.IsLoopback() {
			next.ServeHTTP(w, r)
			return
		}
		for _, ipNet := range nets {
			if ipNet.Contains(ip) {
				next.ServeHTTP(w, r)
				return
			}
		}

		rejectByPolicy(w, r)
	}), nil
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
