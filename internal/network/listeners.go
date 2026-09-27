package network

import (
	"fmt"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal"
)

// Scope words accepted by --http and --device, and the HTTPS modes by --https.
const (
	ScopeLocalhost = "localhost"
	ScopeNetwork   = "network"
)

// Listeners are the listener settings `claw network` can set, each empty
// when left alone. They are the settings a bad save from the Network page
// can flip, and the ones that lock an operator out when they are wrong.
type Listeners struct {
	HTTP   string // localhost | network  -> gateway.host
	HTTPS  string // all | localhost | off -> gateway.tls.mode
	Device string // localhost | network  -> channels.device.host
}

// Empty reports whether nothing is being set.
func (l Listeners) Empty() bool { return l.HTTP == "" && l.HTTPS == "" && l.Device == "" }

// Validate checks the words before anything is written.
func (l Listeners) Validate() error {
	if l.HTTP != "" && l.HTTP != ScopeLocalhost && l.HTTP != ScopeNetwork {
		return fmt.Errorf("--http must be %s or %s, got %q", ScopeLocalhost, ScopeNetwork, l.HTTP)
	}
	if l.Device != "" && l.Device != ScopeLocalhost && l.Device != ScopeNetwork {
		return fmt.Errorf("--device must be %s or %s, got %q", ScopeLocalhost, ScopeNetwork, l.Device)
	}
	switch l.HTTPS {
	case "", config.TLSModeAll, config.TLSModeLocalhost, config.TLSModeOff:
		return nil
	default:
		return fmt.Errorf("--https must be %s, %s or %s, got %q", config.TLSModeAll, config.TLSModeLocalhost, config.TLSModeOff, l.HTTPS)
	}
}

// hostOfScope is the bind address for a scope word.
func hostOfScope(scope string) string {
	if scope == ScopeNetwork {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

// scopeOfHost is the scope word for a bind address: loopback or unset is
// localhost, anything else reaches the network.
func scopeOfHost(host string) string {
	switch strings.TrimSpace(strings.ToLower(host)) {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return ScopeLocalhost
	}
	return ScopeNetwork
}

// ApplyListeners writes the requested listener settings to the config and
// returns the path written. Listener settings are bound when the gateway
// starts, so unlike the allowlist they take effect only after a restart; the
// caller says so.
func ApplyListeners(l Listeners) (string, error) {
	if err := l.Validate(); err != nil {
		return "", err
	}
	path := internal.GetConfigPath()
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return "", err
	}
	if l.HTTP != "" {
		cfg.Gateway.Host = hostOfScope(l.HTTP)
	}
	if l.HTTPS != "" {
		cfg.Gateway.TLS.Mode = l.HTTPS
	}
	if l.Device != "" {
		cfg.Channels.Device.Host = hostOfScope(l.Device)
	}
	if err := config.SaveConfig(path, cfg); err != nil {
		return "", err
	}
	return path, nil
}

// Access is what --show prints: where each listener answers and who may
// reach it, read from the config file (not the running process).
type Access struct {
	HTTP       string // localhost | network
	HTTPPort   int
	HTTPS      string // all | localhost | off
	HTTPSPort  int
	Allowlist  []string
	Device     string // localhost | network, or "" when the channel is off
	DevicePort int
}

// CurrentAccess reads the listener settings from the config.
func CurrentAccess() (Access, error) {
	cfg, err := config.LoadConfig(internal.GetConfigPath())
	if err != nil {
		return Access{}, err
	}
	a := Access{
		HTTP:      scopeOfHost(cfg.Gateway.Host),
		HTTPPort:  cfg.Gateway.EffectivePort(),
		HTTPS:     cfg.Gateway.TLS.EffectiveMode(),
		HTTPSPort: cfg.Gateway.EffectiveTLSPort(),
		Allowlist: cfg.Gateway.AllowedCIDRs,
	}
	if dev := cfg.Channels.Device; dev.Enabled {
		a.Device = scopeOfHost(dev.Host)
		a.DevicePort = dev.Port
	}
	return a, nil
}
