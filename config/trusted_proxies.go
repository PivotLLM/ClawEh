package config

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
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

// Trusts reports whether the peer address (without a port) is a trusted proxy.
func (s *TrustedProxySet) Trusts(peer string) bool {
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
	if !s.Trusts(peer) {
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

// ProxyNotTrusted reports whether requests reach this install through a
// reverse proxy or tunnel on this machine that gateway.trusted_proxies does not
// list, so every request looks like loopback and the allowed networks and the
// login lockout never apply. seenPeer is the loopback peer last seen sending
// forwarding headers ("" when none was); it is true when that peer is not
// trusted. Only an observed request counts: an external URL alone may be a LAN
// address with no proxy at all.
func (c *Config) ProxyNotTrusted(seenPeer string) bool {
	if seenPeer == "" {
		return false
	}
	set, err := CompileTrustedProxies(c.Gateway.TrustedProxies)
	if err != nil {
		// Refused at load and by Store.Update; nothing to judge.
		return false
	}
	return !set.Trusts(seenPeer)
}

// deviceDefaultPort is the device listener's port when channels.device.port
// is unset (device.DefaultDevicePort; config cannot import that package).
const deviceDefaultPort = 18791

// BehindProxy names the reverse proxies this install is reached through: the
// gateway.trusted_proxies entries, then each external URL (gateway.external_url,
// and the device listener's while it is on) that is https/wss but not served by
// ClawEh's own TLS listener for it, which means TLS ends at a proxy. Empty when
// there is none. Where a proxy terminates TLS, plain HTTP on the listener is
// not a finding.
func (c *Config) BehindProxy() []string {
	var out []string
	for _, p := range c.Gateway.TrustedProxies {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	gw := c.Gateway
	gwTLS := gw.TLS.EffectiveMode() != TLSModeOff
	if u := strings.TrimSpace(gw.ExternalURL); proxiedURL(u, gwTLS, gw.EffectiveTLSPort()) {
		out = append(out, u)
	}
	dev := c.Channels.Device
	devPort := dev.Port
	if devPort == 0 {
		devPort = deviceDefaultPort
	}
	if u := strings.TrimSpace(dev.ExternalURL); dev.Enabled && proxiedURL(u, dev.TLS, devPort) {
		out = append(out, u)
	}
	return out
}

// proxiedURL reports whether raw is an https/wss URL that the listener (TLS
// on tlsPort when hasTLS) does not serve itself.
func proxiedURL(raw string, hasTLS bool, tlsPort int) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "wss" {
		return false
	}
	if !hasTLS {
		return true
	}
	port := 443
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	return port != tlsPort
}
