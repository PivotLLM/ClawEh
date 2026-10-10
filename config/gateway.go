package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

type GatewayConfig struct {
	// Host is the plain-HTTP listener's bind address. Loopback ("",
	// 127.0.0.1, localhost, ::1) — the default — binds 127.0.0.1 and [::1]
	// only. Anything else (a LAN address, a hostname, 0.0.0.0) serves plain
	// HTTP on the network too; that is allowed but unencrypted, and the
	// configuration report marks it. The HTTPS listener does not follow Host:
	// it is placed by TLS.Mode. See HTTPBindHosts and HTTPSBindHosts.
	Host string `json:"host" env:"CLAW_GATEWAY_HOST"`
	// Port is the plain-HTTP listener's port (default 18790).
	Port int `json:"port" env:"CLAW_GATEWAY_PORT"`
	// TLSPort is the HTTPS listener's port (default 18443). It is separate from
	// Port because the two listeners bind different addresses; they share one
	// handler chain.
	TLSPort int `json:"tls_port,omitempty" env:"CLAW_GATEWAY_TLS_PORT"`
	// TLS places the HTTPS listener (Mode) and names the certificate it
	// presents. Empty means HTTPS on all interfaces with a self-signed
	// certificate generated under <CLAW_HOME>/tls.
	TLS TLSConfig `json:"tls,omitempty"`
	// ExternalURL is the base URL advertised to external clients (e.g. the
	// claw-auth OAuth utility) for reaching this gateway's HTTP API, and the
	// name the operator browses to. Empty derives it from the listeners (see
	// EffectiveExternalURL); set it to e.g. https://claw.example.com:18443 to
	// reach the WebUI by a host name, or to a reverse proxy's URL. Its host is
	// added to the self-signed certificate and to the accepted Host names.
	ExternalURL string `json:"external_url,omitempty" env:"CLAW_GATEWAY_EXTERNAL_URL"`
	// AllowedCIDRs is the IP allowlist for the shared HTTP server (WebUI/API +
	// health). Empty means loopback only: the WebUI and /api/* have no operator
	// authentication, so nothing off-box reaches them until an allowlist is set
	// deliberately. Binding to 0.0.0.0 does not by itself widen access — the
	// allowlist is a second, independent gate.
	//
	// To reach it from elsewhere, list the networks explicitly, e.g.
	// ["192.168.1.0/24"] or the RFC1918 set (see PrivateNetworkCIDRs). Use
	// ["*"] (AllowAnyAddress) to allow any address in either family — note that
	// "0.0.0.0/0" is an IPv4 prefix and still refuses IPv6 clients. Loopback is
	// always allowed.
	AllowedCIDRs []string `json:"allowed_cidrs,omitempty"`
	// LockoutExempt lists client addresses (IPs or CIDRs) that are never
	// locked out by a per-address lockout: the WebUI login limiter's address
	// table and the device gateway's auth-failure lockout. Loopback is always
	// exempt. Per-account login locks still apply. See LockoutExemptSet.
	LockoutExempt []string `json:"lockout_exempt,omitempty"`
	// TrustedProxies lists reverse-proxy addresses (IPs or CIDRs). A request
	// whose TCP peer is one of them is attributed to the address in its
	// X-Real-IP header (else the first X-Forwarded-For entry) for the IP
	// allowlists, the per-address lockouts, lockout_exempt, logs and the audit
	// log, on the WebUI/API listeners and the device gateway. Those headers are
	// ignored from anyone else. Loopback is not implied. See TrustedProxySet.
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
}

// TLSConfig is the HTTPS listener's placement and certificate. Mode says
// where HTTPS is served (TLSModeAll, the default; TLSModeLocalhost;
// TLSModeOff). CertFile and KeyFile are PEM paths (anywhere on disk) and go
// together: setting one without the other is a config error. With neither set
// the gateway generates and maintains a self-signed certificate under
// <CLAW_HOME>/tls whose names are localhost, the loopback addresses, the host
// name, its FQDN, every non-loopback interface address, the host of
// external_url and ExtraNames.
type TLSConfig struct {
	// Mode is "all" (HTTPS on every interface; empty means this), "localhost"
	// (127.0.0.1 and [::1] only) or "off" (no HTTPS listener).
	Mode     string `json:"mode,omitempty" env:"CLAW_GATEWAY_TLS_MODE"`
	CertFile string `json:"cert_file,omitempty" env:"CLAW_GATEWAY_TLS_CERT_FILE"`
	KeyFile  string `json:"key_file,omitempty"  env:"CLAW_GATEWAY_TLS_KEY_FILE"`
	// ExtraNames are additional DNS names or IP addresses for the self-signed
	// certificate (a DNS alias, a NAT address). Ignored with a user certificate.
	ExtraNames []string `json:"extra_names,omitempty"`
}

// HTTPS listener placements, the values of gateway.tls.mode.
const (
	TLSModeAll       = "all"
	TLSModeLocalhost = "localhost"
	TLSModeOff       = "off"
)

// EffectiveMode is Mode with the default applied: empty means TLSModeAll.
// An unknown value is returned as is; Validate rejects it.
func (t TLSConfig) EffectiveMode() string {
	m := strings.ToLower(strings.TrimSpace(t.Mode))
	if m == "" {
		return TLSModeAll
	}
	return m
}

// UserSupplied reports whether the operator provides the certificate.
func (t TLSConfig) UserSupplied() bool {
	return t.CertFile != "" && t.KeyFile != ""
}

// Validate rejects an unknown mode and a half-configured certificate.
func (t TLSConfig) Validate() error {
	switch t.EffectiveMode() {
	case TLSModeAll, TLSModeLocalhost, TLSModeOff:
	default:
		return fmt.Errorf("gateway.tls.mode %q: must be %q, %q or %q", t.Mode, TLSModeAll, TLSModeLocalhost, TLSModeOff)
	}
	if (t.CertFile == "") != (t.KeyFile == "") {
		return errors.New("gateway.tls.cert_file and gateway.tls.key_file must be set together (or both left empty for a self-signed certificate)")
	}
	return nil
}

// DefaultGatewayPort is the default port for the merged claw HTTP server
// (gateway + WebUI on a single mux). It matches DefaultConfig's Gateway.Port.
const DefaultGatewayPort = 18790

// DefaultGatewayTLSPort is the default port of the HTTPS listener.
const DefaultGatewayTLSPort = 18443

// IsLoopbackHost reports whether host names the local host only. Empty is
// loopback: it is what an unset gateway.host means.
func IsLoopbackHost(host string) bool {
	switch strings.TrimSpace(host) {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	default:
		return false
	}
}

// HTTPSEnabled reports whether the HTTPS listener is on: it is unless
// gateway.tls.mode is "off".
func (g GatewayConfig) HTTPSEnabled() bool {
	return g.TLS.EffectiveMode() != TLSModeOff
}

// EffectivePort is the loopback HTTP port, defaulting to DefaultGatewayPort.
func (g GatewayConfig) EffectivePort() int {
	if g.Port == 0 {
		return DefaultGatewayPort
	}
	return g.Port
}

// EffectiveTLSPort is the HTTPS port, defaulting to DefaultGatewayTLSPort.
func (g GatewayConfig) EffectiveTLSPort() int {
	if g.TLSPort == 0 {
		return DefaultGatewayTLSPort
	}
	return g.TLSPort
}

// Validate rejects listener settings the gateway would refuse to start on.
func (g GatewayConfig) Validate() error {
	if err := g.TLS.Validate(); err != nil {
		return err
	}
	if _, err := CompileLockoutExempt(g.LockoutExempt); err != nil {
		return err
	}
	if _, err := CompileTrustedProxies(g.TrustedProxies); err != nil {
		return err
	}
	for _, p := range []struct {
		key  string
		port int
	}{{"gateway.port", g.Port}, {"gateway.tls_port", g.TLSPort}} {
		if p.port < 0 || p.port > 65535 {
			return fmt.Errorf("%s %d is out of valid range (1-65535)", p.key, p.port)
		}
	}
	if g.HTTPSEnabled() && g.EffectiveTLSPort() == g.EffectivePort() {
		return fmt.Errorf("gateway.tls_port %d must differ from gateway.port", g.EffectiveTLSPort())
	}
	return nil
}

// ValidateMCPHostListen rejects an MCP host listen address that is not
// loopback. The MCP host speaks plain HTTP and is meant for CLI providers on
// this machine; anything off-box goes through the HTTPS gateway or a proxy.
// Empty means the default (127.0.0.1:5911).
func ValidateMCPHostListen(listen string) error {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("mcp_host.listen %q: must be host:port", listen)
	}
	if !IsLoopbackHost(host) {
		return fmt.Errorf("mcp_host.listen %q: the MCP host is plain HTTP and must listen on a loopback address (127.0.0.1 or ::1)", listen)
	}
	return nil
}

// validateListeners is the load-time check for every setting a listener is
// bound from: the gateway refuses to start on a failure here rather than
// coming up half-configured.
func (c *Config) validateListeners() error {
	if err := c.Gateway.Validate(); err != nil {
		return err
	}
	if err := c.Channels.Device.validateTLS(c.Gateway); err != nil {
		return err
	}
	if err := c.Channels.Device.ValidateExposure(); err != nil {
		return err
	}
	return ValidateMCPHostListen(c.MCPHost.Listen)
}

// AllowAnyAddress is the Gateway.AllowedCIDRs entry meaning "any client
// address, IPv4 or IPv6". Mirrors middleware.AllowAnyAddress; declared here so
// the config package does not depend on the HTTP middleware.
const AllowAnyAddress = "*"

// PrivateNetworkCIDRs is the RFC1918 private range set, offered as a
// ready-made allowlist for a LAN install (`claw install --allowed-cidrs`, or
// the WebUI). It is NOT a default: an empty Gateway.AllowedCIDRs means loopback
// only. See EffectiveAllowedCIDRs.
var PrivateNetworkCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
}

// EffectiveAllowedCIDRs returns the IP allowlist to enforce for the shared HTTP
// port. Nil means loopback only, which is the out-of-the-box posture: the
// WebUI/API carry no operator auth, so an install grants no off-box access
// until someone asks for it. A copy is returned so callers cannot mutate the
// configured slice.
func (g GatewayConfig) EffectiveAllowedCIDRs() []string {
	if len(g.AllowedCIDRs) == 0 {
		return nil
	}
	return append([]string(nil), g.AllowedCIDRs...)
}

// ValidateAllowedCIDRs rejects any entry that is not a valid CIDR.
func ValidateAllowedCIDRs(cidrs []string) error {
	for _, c := range cidrs {
		// "*" means any address, in either family — see middleware.AllowAnyAddress.
		// It is not a CIDR, so it has to be accepted before parsing.
		if strings.TrimSpace(c) == AllowAnyAddress {
			continue
		}
		if _, _, err := net.ParseCIDR(c); err != nil {
			return fmt.Errorf("invalid CIDR %q", c)
		}
	}
	return nil
}

// EffectiveExternalURL returns the base URL external clients should use to reach
// the gateway HTTP API. A non-empty ExternalURL is returned verbatim (operators
// may point it at a host name or an https proxy). Otherwise it follows the
// listeners: with HTTPS on all interfaces it is https://<host name>:<tls_port>
// (the self-signed certificate carries the host name; when that is unknown,
// the primary LAN IP); with HTTPS on localhost only it is
// https://127.0.0.1:<tls_port>; with HTTPS off it is the plain-HTTP listener,
// http://<host>:<port>, where a wildcard bind is replaced by the host name as
// above and a loopback one is 127.0.0.1.
func (g GatewayConfig) EffectiveExternalURL() string {
	if g.ExternalURL != "" {
		return g.ExternalURL
	}
	switch g.TLS.EffectiveMode() {
	case TLSModeAll:
		return "https://" + net.JoinHostPort(advertisedHostName(), strconv.Itoa(g.EffectiveTLSPort()))
	case TLSModeLocalhost:
		return "https://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(g.EffectiveTLSPort()))
	}
	host := strings.Trim(strings.TrimSpace(g.Host), "[]")
	switch {
	case IsLoopbackHost(host):
		host = "127.0.0.1"
	case isWildcardHost(host):
		host = advertisedHostName()
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(g.EffectivePort()))
}

// advertisedHostName is the name a wildcard-bound gateway advertises: the
// machine's host name, else its primary LAN IP, else loopback.
func advertisedHostName() string {
	if h, err := os.Hostname(); err == nil {
		if h = strings.TrimSpace(h); h != "" && !IsLoopbackHost(h) {
			return h
		}
	}
	if ip := primaryLANIP(); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

// NetworkAccess reports whether the gateway binds to all interfaces (0.0.0.0),
// i.e. "network access on". Convenience for API/WebUI surfaces.
func (g GatewayConfig) NetworkAccess() bool {
	return g.Host == "0.0.0.0"
}

// primaryLANIP returns the host's first non-loopback private IPv4, or "".
func primaryLANIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil && ip4.IsPrivate() {
			return ip4.String()
		}
	}
	return ""
}
