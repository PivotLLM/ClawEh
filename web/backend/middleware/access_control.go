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
// to be told who may connect.
//
// The entry "*" means any address, in either family. It exists because
// "0.0.0.0/0" is a natural thing to reach for and does NOT mean that: it is an
// IPv4 prefix, so an IPv6 client is still refused by it, and on a dual-stack
// host that reads as the allowlist simply not working. CIDRs are left meaning
// exactly what they say — widening 0.0.0.0/0 to cover IPv6 would fail open,
// which is the wrong direction to guess in — so "*" is the explicit way to say
// "everything", matching allow_from and allow_origins elsewhere in the config.
// AllowAnyAddress is the allowlist entry meaning "any client address, IPv4 or
// IPv6". Spelled the same way as the wildcards in allow_from and allow_origins.
const AllowAnyAddress = "*"

func IPAllowlist(allowedCIDRs []string, next http.Handler) (http.Handler, error) {
	nets := make([]*net.IPNet, 0, len(allowedCIDRs))
	allowAny := false
	for _, cidr := range allowedCIDRs {
		if strings.TrimSpace(cidr) == AllowAnyAddress {
			allowAny = true
			continue
		}
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
		nets = append(nets, ipNet)
	}

	if allowAny {
		return next, nil
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
