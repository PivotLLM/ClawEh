package network

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal"
)

// NewNetworkCommand returns the `claw network` subcommand: the way out of an
// install that listens on the network but refuses every connection from it,
// without hand-editing config.json.
func NewNetworkCommand() *cobra.Command {
	var show bool
	cmd := &cobra.Command{
		Use:   "network [cidrs]",
		Short: "Show or set who may reach the WebUI/API (the gateway IP allowlist)",
		Long: "The WebUI and /api/* have no operator password, so access is gated by an IP\n" +
			"allowlist (gateway.allowed_cidrs) as well as the bind address. Loopback is always\n" +
			"allowed; with no allowlist set, loopback is ALL that is served — which looks like\n" +
			"the port being closed when you browse to it from another machine.\n\n" +
			"With no argument, allows the private LAN ranges (" + fmt.Sprintf("%v", config.PrivateNetworkCIDRs) + ").\n" +
			"Otherwise pass a comma-separated CIDR list, or one of:\n" +
			"  private    the RFC1918 ranges (same as no argument)\n" +
			"  any        any address, IPv4 and IPv6 — the WebUI has no password\n" +
			"  none       loopback only\n\n" +
			"Note 0.0.0.0/0 is an IPv4 prefix and still refuses IPv6 clients; 'any' (\"*\")\n" +
			"covers both families.\n\n" +
			"This edits the config and exits, so it is safe to run while " + internal.BinaryName + " is running:\n" +
			"a running gateway applies the new allowlist on its next config reload — about 15\n" +
			"seconds with the default settings — with no restart.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			spec := "private"
			if len(args) == 1 {
				spec = args[0]
			}
			if show {
				return runShow()
			}
			return runSet(spec)
		},
	}
	cmd.Flags().BoolVar(&show, "show", false, "Print the current allowlist and exit without changing it")
	return cmd
}

func runShow() error {
	cidrs, err := CurrentAllowlist()
	if err != nil {
		return err
	}
	fmt.Printf("Network allowlist: %s\n", Describe(cidrs))
	fmt.Printf("Loopback:          always allowed\n")
	fmt.Printf("Config:            %s\n", internal.GetConfigPath())
	return nil
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
		fmt.Println("WARNING: any address may now reach the WebUI/API, which have no operator password.")
	}
	fmt.Printf("A running %s applies this on its next config reload (about 15s); no restart needed.\n", internal.BinaryName)
	return nil
}
