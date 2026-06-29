package device

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/gatewayproto"
)

// BuildSetupPayload builds the device QR payload. When externalURL is set it is the
// authoritative advertised endpoint (http->ws, https->wss); otherwise the payload
// advertises the detected LAN IPs on the device listener port.
func BuildSetupPayload(externalURL string, lanIPs []string, devicePort int, token string) (gatewayproto.SetupPayload, error) {
	externalURL = strings.TrimSpace(externalURL)
	if externalURL == "" {
		return gatewayproto.NewSetupPayload(lanIPs, devicePort, token, gatewayproto.SetupProtocolWS), nil
	}
	u, err := url.Parse(externalURL)
	if err != nil || u.Hostname() == "" {
		return gatewayproto.SetupPayload{}, fmt.Errorf("device: invalid external_url %q", externalURL)
	}
	proto := gatewayproto.SetupProtocolWS
	port := 80
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		proto, port = gatewayproto.SetupProtocolWSS, 443
	}
	if p := u.Port(); p != "" {
		if pi, perr := strconv.Atoi(p); perr == nil {
			port = pi
		}
	}
	return gatewayproto.NewSetupPayload([]string{u.Hostname()}, port, token, proto), nil
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
