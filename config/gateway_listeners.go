// ClawEh
// License: MIT

package config

import (
	"net"
	"strconv"
	"strings"
)

// The gateway serves one handler chain on two listeners: plain HTTP, placed
// by gateway.host, and HTTPS, placed by gateway.tls.mode. The helpers below
// are the single description of where each binds and which URLs reach it, so
// the gateway, `claw status`, the WebUI API and the report agree.

// isWildcardHost reports whether host is an all-interfaces bind address.
func isWildcardHost(host string) bool {
	switch strings.TrimSpace(host) {
	case "0.0.0.0", "::", "[::]":
		return true
	default:
		return false
	}
}

// HTTPOnNetwork reports whether plain HTTP is served beyond this machine,
// i.e. gateway.host is not a loopback address.
func (g GatewayConfig) HTTPOnNetwork() bool {
	return !IsLoopbackHost(g.Host)
}

// HTTPSOnNetwork reports whether HTTPS is served on every interface
// (gateway.tls.mode "all").
func (g GatewayConfig) HTTPSOnNetwork() bool {
	return g.TLS.EffectiveMode() == TLSModeAll
}

// ReachableOffBox reports whether any gateway listener accepts connections
// from other machines (subject to the IP allowlist).
func (g GatewayConfig) ReachableOffBox() bool {
	return g.HTTPOnNetwork() || g.HTTPSOnNetwork()
}

// HTTPBindHosts are the addresses the plain-HTTP listener binds on Port.
// Loopback is always among them, since local tools and `claw status` use
// http://127.0.0.1:<port>: a loopback Host is 127.0.0.1 and ::1; a wildcard
// Host is the wildcard alone (it covers loopback, and Go binds it dual-stack);
// any other Host is the loopback pair plus Host.
func (g GatewayConfig) HTTPBindHosts() []string {
	host := strings.Trim(strings.TrimSpace(g.Host), "[]")
	switch {
	case IsLoopbackHost(host):
		return []string{"127.0.0.1", "::1"}
	case isWildcardHost(host):
		return []string{"0.0.0.0"}
	default:
		return []string{"127.0.0.1", "::1", host}
	}
}

// HTTPSBindHosts are the addresses the HTTPS listener binds on TLSPort: the
// wildcard for mode "all" (Go binds 0.0.0.0 dual-stack, so IPv6 clients are
// served too), the loopback pair for "localhost", none for "off".
func (g GatewayConfig) HTTPSBindHosts() []string {
	switch g.TLS.EffectiveMode() {
	case TLSModeAll:
		return []string{"0.0.0.0"}
	case TLSModeLocalhost:
		return []string{"127.0.0.1", "::1"}
	default:
		return nil
	}
}

// NetworkHosts turns a bind host into the addresses a browser on the network
// would use: a wildcard is expanded to every non-loopback, non-link-local
// IPv4 interface address (the bind host itself when there is none); a
// specific address is returned as is.
func NetworkHosts(bind string) []string {
	if !isWildcardHost(bind) {
		return []string{strings.Trim(strings.TrimSpace(bind), "[]")}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return []string{bind}
	}
	var hosts []string
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			hosts = append(hosts, ip4.String())
		}
	}
	if len(hosts) == 0 {
		return []string{bind}
	}
	return hosts
}

// baseURL renders scheme://host:port/.
func baseURL(scheme, host string, port int) string {
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/"
}

// LocalHTTPURL is the plain-HTTP URL on this machine, always served.
func (g GatewayConfig) LocalHTTPURL() string {
	return baseURL("http", "127.0.0.1", g.EffectivePort())
}

// NetworkHTTPURLs are the plain-HTTP URLs other machines use, or nil when
// plain HTTP is loopback only.
func (g GatewayConfig) NetworkHTTPURLs() []string {
	if !g.HTTPOnNetwork() {
		return nil
	}
	var out []string
	for _, h := range NetworkHosts(g.Host) {
		out = append(out, baseURL("http", h, g.EffectivePort()))
	}
	return out
}

// HTTPSURLs are the HTTPS URLs to open: one per network address for mode
// "all", https://127.0.0.1:<tls_port>/ for "localhost", nil for "off".
func (g GatewayConfig) HTTPSURLs() []string {
	switch g.TLS.EffectiveMode() {
	case TLSModeAll:
		hosts := NetworkHosts("0.0.0.0")
		out := make([]string, 0, len(hosts))
		for _, h := range hosts {
			out = append(out, baseURL("https", h, g.EffectiveTLSPort()))
		}
		return out
	case TLSModeLocalhost:
		return []string{baseURL("https", "127.0.0.1", g.EffectiveTLSPort())}
	default:
		return nil
	}
}

// ListenerSettings are the gateway settings that are applied only when the
// listeners are bound, at start: comparing the running gateway's snapshot
// with the saved config tells whether a restart is needed.
type ListenerSettings struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLSPort  int    `json:"tls_port"`
	TLSMode  string `json:"mode"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

// Listeners returns the listener settings with defaults applied, so an
// absent key and its default compare equal; settings that bind nothing (the
// HTTPS port and certificate when HTTPS is off) are left zero.
func (g GatewayConfig) Listeners() ListenerSettings {
	s := ListenerSettings{
		Host:    strings.TrimSpace(g.Host),
		Port:    g.EffectivePort(),
		TLSPort: g.EffectiveTLSPort(),
		TLSMode: g.TLS.EffectiveMode(),
	}
	if IsLoopbackHost(s.Host) {
		s.Host = "127.0.0.1"
	}
	switch {
	case !g.HTTPSEnabled():
		s.TLSPort = 0 // no HTTPS listener: its port and certificate do not matter
	case g.TLS.UserSupplied():
		s.CertFile = strings.TrimSpace(g.TLS.CertFile)
		s.KeyFile = strings.TrimSpace(g.TLS.KeyFile)
	}
	return s
}
