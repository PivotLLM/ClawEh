// ClawEh
// License: MIT

package status

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/PivotLLM/ClawEh/channels/device"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/admin"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
)

// accessReport answers the question an operator on a headless machine asks
// first: what URL do I open, will the browser warn about the certificate, and
// can I log in. It is derived from the configuration, not from the live
// process, so it also works before the first start.
func accessReport(cfg *config.Config) string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	gw := cfg.Gateway
	line("Access:")
	line("  WebUI (localhost):  %s", gw.LocalHTTPURL())
	for _, u := range gw.NetworkHTTPURLs() {
		line("  WebUI (network):    %s  (plain HTTP, unencrypted)", u)
	}
	switch gw.TLS.EffectiveMode() {
	case config.TLSModeAll:
		for _, u := range gw.HTTPSURLs() {
			line("  WebUI (network):    %s", u)
		}
	case config.TLSModeLocalhost:
		for _, u := range gw.HTTPSURLs() {
			line("  WebUI (localhost):  %s", u)
		}
	default:
		line("  HTTPS:              off (gateway.tls.mode \"off\")")
	}
	if !gw.ReachableOffBox() {
		line("  WebUI (network):    not served — plain HTTP is on localhost only and HTTPS is")
		line("                      %s; set gateway.tls.mode to \"all\" to serve it", httpsScope(gw))
	}
	if gw.HTTPSEnabled() {
		certificateLines(&b, cfg)
	}
	if gw.ExternalURL != "" {
		line("  External URL:       %s", gw.ExternalURL)
	}
	if cfg.Channels.Device.Enabled {
		port := cfg.Channels.Device.Port
		if port == 0 {
			port = device.DefaultDevicePort
		}
		host := cfg.Channels.Device.Host
		if host == "" {
			host = "127.0.0.1"
		}
		line("  Device listener:    %s://%s/", cfg.Channels.Device.WebSocketScheme(), net.JoinHostPort(host, strconv.Itoa(port)))
	}
	adminLine(&b, cfg)
	return b.String()
}

// httpsScope says where HTTPS is served, for the "not served" line.
func httpsScope(gw config.GatewayConfig) string {
	if gw.HTTPSEnabled() {
		return "on localhost only"
	}
	return "off"
}

// certificateLines names the certificate the HTTPS listener serves so the
// operator can match the browser's warning against it. A self-signed pair
// that has not been generated yet (the gateway has never started with HTTPS)
// is reported as such rather than as an error.
func certificateLines(b *strings.Builder, cfg *config.Config) {
	info, err := tlscert.InspectFile(tlscert.OptionsFromConfig(cfg))
	switch {
	case errors.Is(err, os.ErrNotExist):
		b.WriteString("  Certificate:        not generated yet (created on first HTTPS start)\n")
		return
	case err != nil:
		fmt.Fprintf(b, "  Certificate:        unreadable: %v\n", err)
		return
	}
	fmt.Fprintf(b, "  Certificate:        %s, expires %s\n", info.Source, info.NotAfter.Format("2006-01-02"))
	if info.Source == tlscert.SourceSelfSigned {
		b.WriteString("                      (self-signed: the browser warns once; compare this fingerprint)\n")
	}
	fmt.Fprintf(b, "  Fingerprint:        %s\n", info.Fingerprint)
}

// adminLine reports whether a login exists, since without one the WebUI shows
// only the "run claw admin" page.
func adminLine(b *strings.Builder, cfg *config.Config) {
	path := admin.Path(cfg.DataDir())
	creds, err := admin.Load(path)
	switch {
	case err == nil:
		fmt.Fprintf(b, "  Admin account:      %s ✓ (%s)\n", creds.Username, path)
	case errors.Is(err, admin.ErrNotConfigured):
		fmt.Fprintf(b, "  Admin account:      none — run: claw admin  (%s)\n", path)
	default:
		if perr, ok := errors.AsType[*admin.PermissionError](err); ok {
			fmt.Fprintf(b, "  Admin account:      ignored, file readable by others — run: %s\n", perr.Fix())
			return
		}
		fmt.Fprintf(b, "  Admin account:      unreadable: %v\n", err)
	}
}
