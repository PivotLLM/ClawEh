package device

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/gatewayproto"
)

// BuildSetupPayload builds the device QR payload. When externalURL is set it is the
// authoritative advertised endpoint (http->ws, https->wss); otherwise the payload
// advertises the hostname of gatewayExternalURL (when set) followed by the detected
// LAN IPs, on the device listener port, over wss when useTLS (channels.device.tls)
// is on and ws otherwise.
func BuildSetupPayload(externalURL, gatewayExternalURL string, lanIPs []string, devicePort int, token string, useTLS bool) (gatewayproto.SetupPayload, error) {
	externalURL = strings.TrimSpace(externalURL)
	if externalURL == "" {
		proto := gatewayproto.SetupProtocolWS
		if useTLS {
			proto = gatewayproto.SetupProtocolWSS
		}
		return gatewayproto.NewSetupPayload(advertisedHosts(gatewayExternalURL, lanIPs), devicePort, token, proto), nil
	}
	proto, host, port, err := externalEndpoint(externalURL)
	if err != nil {
		return gatewayproto.SetupPayload{}, err
	}
	return gatewayproto.NewSetupPayload([]string{host}, port, token, proto), nil
}

// ConnectURL returns the address devices connect to, as advertised by the QR:
// <ws|wss>://<host>:<port>. It never returns an empty string: with no external
// URL the host falls back to gateway.external_url's hostname, then the first LAN
// IP, then 127.0.0.1.
func ConnectURL(dev config.DeviceChannelConfig, gatewayExternalURL string, lanIPs []string) string {
	if ext := strings.TrimSpace(dev.ExternalURL); ext != "" {
		if proto, host, port, err := externalEndpoint(ext); err == nil {
			return proto + "://" + net.JoinHostPort(host, strconv.Itoa(port))
		}
	}
	proto := gatewayproto.SetupProtocolWS
	if dev.TLS {
		proto = gatewayproto.SetupProtocolWSS
	}
	port := dev.Port
	if port == 0 {
		port = DefaultDevicePort
	}
	host := "127.0.0.1"
	if hosts := advertisedHosts(gatewayExternalURL, lanIPs); len(hosts) > 0 {
		host = hosts[0]
	}
	return proto + "://" + net.JoinHostPort(host, strconv.Itoa(port))
}

// externalEndpoint parses channels.device.external_url into the scheme the QR
// advertises (http/ws -> ws, https/wss -> wss), its hostname and its port
// (explicit, else 80 or 443).
func externalEndpoint(externalURL string) (proto, host string, port int, err error) {
	u, err := url.Parse(externalURL)
	if err != nil || u.Hostname() == "" {
		return "", "", 0, fmt.Errorf("device: invalid external_url %q", externalURL)
	}
	proto, port = gatewayproto.SetupProtocolWS, 80
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		proto, port = gatewayproto.SetupProtocolWSS, 443
	}
	if p := u.Port(); p != "" {
		if pi, perr := strconv.Atoi(p); perr == nil {
			port = pi
		}
	}
	return proto, u.Hostname(), port, nil
}

// advertisedHosts is the host list the QR advertises without an external URL: the
// hostname of gateway.external_url first, when the operator set one, then the
// LAN IPs. Clients try each entry in order.
func advertisedHosts(gatewayExternalURL string, lanIPs []string) []string {
	hosts := []string{}
	if u, err := url.Parse(strings.TrimSpace(gatewayExternalURL)); err == nil && u.Hostname() != "" {
		hosts = append(hosts, u.Hostname())
	}
	return append(hosts, lanIPs...)
}

// GenerateSharedToken returns a 32-byte random hex token (matches the Rabbit
// setup script's `openssl rand -hex 32`), used as the gateway shared auth token.
func GenerateSharedToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("device: generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// EnsureProvisioned loads the config and ensures the device gateway is usable for
// pairing: a shared token exists and the channel is enabled. It persists the config
// only when something changed. Returns the (possibly updated) config and whether a
// write occurred. The caller is responsible for triggering a gateway reload.
func EnsureProvisioned(configPath string) (cfg *config.Config, changed bool, err error) {
	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		return nil, false, err
	}
	if cfg.Channels.Device.Token == "" {
		tok, terr := GenerateSharedToken()
		if terr != nil {
			return nil, false, terr
		}
		cfg.Channels.Device.Token = tok
		changed = true
	}
	if cfg.Channels.Device.WordToken == "" {
		wtok, werr := GenerateWordToken()
		if werr != nil {
			return nil, false, werr
		}
		cfg.Channels.Device.WordToken = wtok
		changed = true
	}
	if !cfg.Channels.Device.Enabled {
		cfg.Channels.Device.Enabled = true
		changed = true
	}
	if changed {
		if err = config.SaveConfig(configPath, cfg); err != nil {
			return nil, false, err
		}
	}
	return cfg, changed, nil
}

// LANIPv4s returns routable LAN IPv4 addresses (excludes loopback, link-local, and
// the Docker default bridge), mirroring the Rabbit setup script's IP detection.
func LANIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return []string{}
	}
	out := []string{}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}
			if ip4[0] == 127 || (ip4[0] == 169 && ip4[1] == 254) || (ip4[0] == 172 && ip4[1] == 17) {
				continue
			}
			out = append(out, ip4.String())
		}
	}
	return out
}

// IsLoopbackHost reports whether a gateway bind host would be unreachable from LAN
// devices (so pairing can warn the operator to bind on 0.0.0.0 or a LAN IP).
func IsLoopbackHost(host string) bool {
	switch host {
	case "", "127.0.0.1", "localhost", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
