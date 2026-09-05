// Package network implements the `claw network` CLI: read and set the IP
// allowlist that guards the shared HTTP port (WebUI, /api/*, health). It edits
// config.json and exits, so it is safe to run against a running gateway — the
// reload watcher applies the new allowlist to the live listener within a few
// seconds, with no restart and without dropping WebSocket connections.
package network

import (
	"fmt"
	"strings"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal"
)

// Aliases expand to a full CIDR list, so the common choices do not require
// remembering three RFC1918 prefixes — or that "*" rather than 0.0.0.0/0 is the
// spelling that covers IPv6 as well.
var Aliases = map[string][]string{
	"private":  config.PrivateNetworkCIDRs,
	"lan":      config.PrivateNetworkCIDRs,
	"any":      {config.AllowAnyAddress},
	"all":      {config.AllowAnyAddress},
	"none":     {},
	"loopback": {},
}

// ParseAllowlist turns a comma-separated CIDR list into a slice, expanding an
// alias if one was given. The result is not validated; call ApplyAllowlist or
// config.ValidateAllowedCIDRs.
func ParseAllowlist(csv string) []string {
	if expanded, ok := Aliases[strings.ToLower(strings.TrimSpace(csv))]; ok {
		return append([]string(nil), expanded...)
	}
	parts := strings.Split(csv, ",")
	cidrs := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			cidrs = append(cidrs, t)
		}
	}
	return cidrs
}

// CurrentAllowlist reads the allowlist already in the config.
func CurrentAllowlist() ([]string, error) {
	cfg, err := config.LoadConfig(internal.GetConfigPath())
	if err != nil {
		return nil, err
	}
	return cfg.Gateway.AllowedCIDRs, nil
}

// ApplyAllowlist validates cidrs and writes them to gateway.allowed_cidrs,
// returning the config path written. The caller reports the outcome; this does
// no printing so `claw install` and `claw network` can word it differently.
func ApplyAllowlist(cidrs []string) (string, error) {
	path := internal.GetConfigPath()
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return "", err
	}
	if err := config.ValidateAllowedCIDRs(cidrs); err != nil {
		return "", err
	}
	cfg.Gateway.AllowedCIDRs = cidrs
	if err := config.SaveConfig(path, cfg); err != nil {
		return "", err
	}
	return path, nil
}

// Describe renders an allowlist for an operator reading a terminal.
func Describe(cidrs []string) string {
	if len(cidrs) == 0 {
		return "none — loopback only"
	}
	if len(cidrs) == 1 && strings.TrimSpace(cidrs[0]) == config.AllowAnyAddress {
		return fmt.Sprintf("%v — any address, IPv4 and IPv6", cidrs)
	}
	return fmt.Sprintf("%v", cidrs)
}
