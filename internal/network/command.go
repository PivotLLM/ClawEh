package network

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal"
)

// NewNetworkCommand returns the `claw network` subcommand: the way back into
// an install whose listeners or allowlist keep the operator out, without
// hand-editing config.json.
func NewNetworkCommand() *cobra.Command {
	var (
		show bool
		l    Listeners
	)
	cmd := &cobra.Command{
		Use:   "network [cidrs]",
		Short: "Show or set network access to the WebUI/API: allowlist, HTTP/HTTPS scope, device listener",
		Long: "Two things decide whether another machine can reach the WebUI and /api/*:\n" +
			"where the listeners bind (this machine only, or the network) and the IP\n" +
			"allowlist (gateway.allowed_cidrs). Loopback is always allowed. With no allowlist,\n" +
			"loopback is all that is served, which looks like the port being closed, or\n" +
			"\"Forbidden\", from another machine. Logging in is required either way.\n\n" +
			"The argument sets the allowlist. With no argument, it allows the private LAN\n" +
			"ranges (" + fmt.Sprintf("%v", config.PrivateNetworkCIDRs) + "). Otherwise pass a\n" +
			"comma-separated CIDR list, or one of:\n" +
			"  private    the RFC1918 ranges (same as no argument)\n" +
			"  any        any address, IPv4 and IPv6\n" +
			"  none       loopback only\n\n" +
			"Note 0.0.0.0/0 is an IPv4 prefix and still refuses IPv6 clients; 'any' (\"*\")\n" +
			"covers both families.\n\n" +
			"The flags set where the listeners bind:\n" +
			"  --http localhost|network     plain HTTP (gateway.host)\n" +
			"  --https all|localhost|off    HTTPS (gateway.tls.mode)\n" +
			"  --device localhost|network   the device listener (channels.device.host)\n\n" +
			"This edits the config and exits, so it is safe to run while " + internal.BinaryName + " is running.\n" +
			"A running " + internal.BinaryName + " applies a new allowlist on its next config reload (about 15\n" +
			"seconds) with no restart; the listener flags take effect after a restart.\n\n" +
			"Examples:\n" +
			"  " + internal.BinaryName + " network --show\n" +
			"  " + internal.BinaryName + " network                          # allow the private LAN ranges\n" +
			"  " + internal.BinaryName + " network 192.168.1.0/24 --http network\n" +
			"  " + internal.BinaryName + " network none --http localhost --https localhost",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if show {
				return runShow()
			}
			setAllowlist := len(args) == 1 || l.Empty()
			spec := "private"
			if len(args) == 1 {
				spec = args[0]
			}
			if setAllowlist {
				if err := runSet(spec); err != nil {
					return err
				}
			}
			if !l.Empty() {
				return runListeners(l)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&show, "show", false, "Print the current access settings and exit without changing anything")
	cmd.Flags().StringVar(&l.HTTP, "http", "", "Where plain HTTP listens: localhost or network")
	cmd.Flags().StringVar(&l.HTTPS, "https", "", "Where HTTPS listens: all, localhost or off")
	cmd.Flags().StringVar(&l.Device, "device", "", "Where the device listener listens: localhost or network")
	return cmd
}

func runShow() error {
	a, err := CurrentAccess()
	if err != nil {
		return err
	}
	fmt.Printf("HTTP:              %s (port %d)\n", a.HTTP, a.HTTPPort)
	if a.HTTPS == config.TLSModeOff {
		fmt.Printf("HTTPS:             off\n")
	} else {
		fmt.Printf("HTTPS:             %s (port %d)\n", httpsScope(a.HTTPS), a.HTTPSPort)
	}
	fmt.Printf("Network allowlist: %s\n", Describe(a.Allowlist))
	fmt.Printf("Loopback:          always allowed\n")
	if a.Device == "" {
		fmt.Printf("Device listener:   disabled\n")
	} else {
		fmt.Printf("Device listener:   %s (port %d)\n", a.Device, a.DevicePort)
	}
	fmt.Printf("Config:            %s\n", internal.GetConfigPath())
	return nil
}

// httpsScope words the HTTPS mode the way the HTTP scope is worded.
func httpsScope(mode string) string {
	if mode == config.TLSModeAll {
		return ScopeNetwork
	}
	return mode
}

func runSet(spec string) error {
	cidrs := ParseAllowlist(spec)
	path, err := ApplyAllowlist(cidrs)
	if err != nil {
		return err
	}
	fmt.Printf("Network allowlist set to %s\n", Describe(cidrs))
	fmt.Printf("Loopback is always allowed. Written to %s\n", path)
	if len(cidrs) == 1 && cidrs[0] == config.AllowAnyAddress {
		fmt.Println("WARNING: any address may now reach the login page.")
	}
	fmt.Printf("A running %s applies this on its next config reload (about 15s); no restart needed.\n", internal.BinaryName)
	return nil
}

func runListeners(l Listeners) error {
	path, err := ApplyListeners(l)
	if err != nil {
		return err
	}
	var set []string
	if l.HTTP != "" {
		set = append(set, "HTTP "+l.HTTP)
	}
	if l.HTTPS != "" {
		set = append(set, "HTTPS "+l.HTTPS)
	}
	if l.Device != "" {
		set = append(set, "device listener "+l.Device)
	}
	fmt.Printf("Listeners set: %s. Written to %s\n", strings.Join(set, ", "), path)
	fmt.Printf("Listeners are bound at start: restart %s for this to take effect.\n", internal.BinaryName)
	return nil
}
