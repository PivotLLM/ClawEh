// ClawEh
// License: MIT

package report

import (
	"context"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/channels/device"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
	"github.com/PivotLLM/ClawEh/mcpserver"
)

const offHostNote = "reachable from other hosts, subject to the client allowlist"

// listener is one bound socket and how it gates clients.
type listener struct {
	Name, Addr, Allow, Notes string
	Enabled                  bool
}

// listeners describes every socket the process may bind, from config.
func listeners(cfg *config.Config) []listener {
	gw := cfg.Gateway
	gwNotes := []string{}
	if gw.ReachableOffBox() {
		gwNotes = append(gwNotes, offHostNote)
	}
	if gw.HTTPOnNetwork() {
		gwNotes = append(gwNotes, "plain HTTP on the network (gateway.host)")
	}
	switch gw.TLS.EffectiveMode() {
	case config.TLSModeLocalhost:
		gwNotes = append(gwNotes, "HTTPS on localhost only (gateway.tls.mode \"localhost\")")
	case config.TLSModeOff:
		gwNotes = append(gwNotes, "HTTPS off (gateway.tls.mode \"off\")")
	}
	if gw.ExternalURL != "" {
		gwNotes = append(gwNotes, "external_url "+gw.ExternalURL)
	}
	out := make([]listener, 0, 4)
	out = append(out, listener{
		Name:    webAPIName,
		Addr:    gatewayAddrs(gw),
		Allow:   gatewayAllow(gw.EffectiveAllowedCIDRs()),
		Notes:   strings.Join(gwNotes, "; "),
		Enabled: true,
	})

	mcp := listener{Name: "MCP host", Enabled: cfg.MCPHostEffectivelyEnabled()}
	if mcp.Enabled {
		listen := orValue(cfg.MCPHost.Listen, mcpserver.DefaultListen)
		host, _, _ := strings.Cut(listen, ":")
		mcp.Addr = listen + orValue(cfg.MCPHost.EndpointPath, "")
		mcp.Allow = "per-agent service tokens"
		notes := []string{}
		if !isLoopback(host) {
			notes = append(notes, "reachable from other hosts")
		}
		if !cfg.MCPHost.Enabled {
			notes = append(notes, "enabled by auto_enable because a CLI provider is configured")
		}
		mcp.Notes = strings.Join(notes, "; ")
	}
	out = append(out, mcp)

	dev := cfg.Channels.Device
	dl := listener{Name: "Device listener", Enabled: dev.Enabled}
	if dev.Enabled {
		port := dev.Port
		if port == 0 {
			port = device.DefaultDevicePort
		}
		host := orValue(dev.Host, "127.0.0.1")
		dl.Addr = bindAddr(host, port)
		dl.Allow = "any address (device token required; loopback always allowed)"
		if len(dev.AllowedCIDRs) > 0 {
			dl.Allow = strings.Join(dev.AllowedCIDRs, ", ") + " (loopback always allowed)"
		}
		notes := []string{}
		if !isLoopback(host) {
			notes = append(notes, offHostNote)
		}
		if dev.TLS {
			notes = append(notes, "wss:// with the WebUI HTTPS certificate (channels.device.tls)")
		} else {
			notes = append(notes, "ws://, unencrypted")
		}
		if dev.ExternalURL != "" {
			notes = append(notes, "external_url "+dev.ExternalURL)
		}
		if dev.AutoApprove {
			notes = append(notes, "auto_approve: pairings need no operator approval")
		}
		dl.Notes = strings.Join(notes, "; ")
	}
	out = append(out, dl)

	line := cfg.Channels.LINE
	ll := listener{Name: "LINE webhook", Enabled: line.Enabled}
	if line.Enabled {
		ll.Addr = bindAddr(line.WebhookHost, line.WebhookPort) + line.WebhookPath
		ll.Allow = "LINE request signature (channel_secret)"
		if !isLoopback(line.WebhookHost) {
			ll.Notes = "reachable from other hosts (a webhook must be)"
		}
	}
	out = append(out, ll)
	return out
}

// webAPIName is the WebUI and HTTP API listener pair, named for a reader who
// knows the product, not its process layout.
const webAPIName = "WebUI/API"

// gatewayAddrs lists every address the WebUI/API binds, each tagged with
// its protocol: "127.0.0.1:18790 (HTTP), [::1]:18790 (HTTP), 0.0.0.0:18443
// (HTTPS)".
func gatewayAddrs(gw config.GatewayConfig) string {
	httpHosts, httpsHosts := gw.HTTPBindHosts(), gw.HTTPSBindHosts()
	addrs := make([]string, 0, len(httpHosts)+len(httpsHosts))
	for _, h := range httpHosts {
		addrs = append(addrs, net.JoinHostPort(h, itoa(gw.EffectivePort()))+" (HTTP)")
	}
	for _, h := range httpsHosts {
		addrs = append(addrs, net.JoinHostPort(h, itoa(gw.EffectiveTLSPort()))+" (HTTPS)")
	}
	return strings.Join(addrs, ", ")
}

// tlsSummary describes the HTTPS listener's certificate: its source, names,
// expiry and fingerprint, read from the certificate file the gateway uses.
func tlsSummary(cfg *config.Config) string {
	if !cfg.Gateway.HTTPSEnabled() {
		return "off (gateway.tls.mode is \"off\": the WebUI/API is plain HTTP only)"
	}
	opts := tlscert.OptionsFromConfig(cfg)
	info, err := tlscert.InspectFile(opts)
	if err != nil {
		certPath, _ := opts.Paths()
		return string(opts.Source()) + " certificate " + certPath + " (unreadable: " + err.Error() + ")"
	}
	return string(info.Source) + " certificate " + info.CertFile +
		"; names " + strings.Join(info.Names(), ", ") +
		"; expires " + info.NotAfter.Format(time.RFC3339) +
		"; SHA-256 " + info.Fingerprint
}

// gatewayAllow explains the gateway allowlist: nil is loopback only, "*" is
// any address, otherwise the listed networks (loopback is always allowed).
func gatewayAllow(cidrs []string) string {
	if len(cidrs) == 0 {
		return "loopback only (allowed_cidrs is empty)"
	}
	if slices.Contains(cidrs, config.AllowAnyAddress) {
		return "any address (allowed_cidrs contains *)"
	}
	return strings.Join(cidrs, ", ") + " (loopback always allowed)"
}

func collectNetwork(_ context.Context, cfg *config.Config, _ Environment) Section {
	lt := Table{Caption: "Listeners", Columns: []string{"Listener", "Bind address", "Client allowlist", "Notes"}}
	for _, l := range listeners(cfg) {
		if !l.Enabled {
			lt.Rows = append(lt.Rows, row(l.Name, disabled, "", ""))
			continue
		}
		lt.Rows = append(lt.Rows, row(l.Name, l.Addr, l.Allow, l.Notes))
	}

	pt := pairs("Origins and proxies",
		row("TLS", tlsSummary(cfg)),
		row("Device listener allowed origins", joinOr(cfg.Channels.Device.AllowOrigins, "(none configured)")),
		row("Web tools proxy", orValue(redactURL(cfg.Tools.Web.Proxy), none)),
	)
	for _, p := range cfg.Providers {
		if p.Proxy != "" {
			pt.Rows = append(pt.Rows, row("Provider "+p.Name+" proxy", redactURL(p.Proxy)))
		}
	}
	for _, b := range cfg.Channels.Telegram {
		if b.Enabled && b.Proxy != "" {
			pt.Rows = append(pt.Rows, row("Telegram "+b.ChannelName()+" proxy", redactURL(b.Proxy)))
		}
	}
	if cfg.Channels.Discord.Enabled && cfg.Channels.Discord.Proxy != "" {
		pt.Rows = append(pt.Rows, row("Discord proxy", redactURL(cfg.Channels.Discord.Proxy)))
	}

	return Section{
		Title: "Network",
		Notes: []string{
			"The WebUI and the HTTP API (WebUI/API) are served on two listeners: plain HTTP where gateway.host " +
				"says (loopback by default) and HTTPS where gateway.tls.mode says (all interfaces by default). " +
				"The MCP host and the device listener each bind their own. A listener on a loopback address is reachable only from this host. " +
				"The network allowlist (gateway.allowed_cidrs) is a second gate independent of the bind address: " +
				"empty means loopback only, * means any address.",
		},
		Tables: []Table{lt, pt},
	}
}
