package config

import (
	"net"
	"net/netip"
	"strings"
)

// TrustedProxySet is a compiled gateway.trusted_proxies: the peer addresses
// whose X-Real-IP / X-Forwarded-For headers name the real client. Unlike
// LockoutExemptSet it holds only what was listed; loopback is not implied. It
// is immutable once built, so a reload can swap in a new one. A nil set trusts
// no one.
type TrustedProxySet struct {
	prefixes []netip.Prefix
}

// CompileTrustedProxies parses entries, each an IP address or a CIDR, with the
// same rules as gateway.lockout_exempt.
func CompileTrustedProxies(entries []string) (*TrustedProxySet, error) {
	prefixes, err := compileAddrPrefixes("gateway.trusted_proxies", entries)
	if err != nil {
		return nil, err
	}
	return &TrustedProxySet{prefixes: prefixes}, nil
}

// trusts reports whether the peer address (without a port) is a trusted proxy.
func (s *TrustedProxySet) trusts(peer string) bool {
	if s == nil {
		return false
	}
	addr, err := netip.ParseAddr(peer)
	if err != nil {
		return false
	}
	addr = addr.WithZone("").Unmap()
	for _, p := range s.prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// ClientAddr returns the client address (without a port) of a request whose
// TCP peer is remoteAddr. When the peer is a trusted proxy, the address comes
// from realIP (the X-Real-IP header) or, failing that, from the first entry of
// forwardedFor (X-Forwarded-For); a header that does not parse as an IP address
// is ignored. Otherwise, or when neither header helps, it is the peer itself.
func (s *TrustedProxySet) ClientAddr(remoteAddr, realIP, forwardedFor string) string {
	peer := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		peer = h
	}
	if !s.trusts(peer) {
		return peer
	}
	if addr, ok := parseForwardedAddr(realIP); ok {
		return addr
	}
	first, _, _ := strings.Cut(forwardedFor, ",")
	if addr, ok := parseForwardedAddr(first); ok {
		return addr
	}
	return peer
}

// parseForwardedAddr parses one forwarded address and returns it in canonical
// form (an IPv4-mapped IPv6 address becomes IPv4).
func parseForwardedAddr(v string) (string, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(v))
	if err != nil || addr.Zone() != "" {
		return "", false
	}
	return addr.Unmap().String(), true
}
