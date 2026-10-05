// ClawEh
// License: MIT

package tlscert

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/internal"
)

// NewTLSCommand returns `claw tls`: it prints the certificate the HTTPS
// listener presents so an operator can check the fingerprint their browser
// shows before accepting a self-signed certificate, and --regenerate replaces
// the self-signed pair.
func NewTLSCommand() *cobra.Command {
	var regenerate bool
	cmd := &cobra.Command{
		Use:   "tls",
		Short: "Show the HTTPS listener's certificate (source, names, expiry, fingerprint)",
		Long: internal.BinaryName + " serves plain HTTP on gateway.host:gateway.port (loopback by default) and\n" +
			"HTTPS on gateway.tls_port (default 18443) where gateway.tls.mode says: \"all\" interfaces\n" +
			"(the default), \"localhost\" only, or \"off\". HTTPS presents either the operator's\n" +
			"certificate (gateway.tls.cert_file and key_file) or a self-signed one it generates\n" +
			"under <CLAW_HOME>/tls and renews itself.\n\n" +
			"This prints that certificate. Compare the SHA-256 fingerprint with the one your\n" +
			"browser shows before accepting a self-signed certificate.\n\n" +
			"--regenerate replaces the self-signed certificate now, for example after adding\n" +
			"gateway.tls.extra_names. A running " + internal.BinaryName + " picks the new pair up within a minute;\n" +
			"browsers that accepted the old certificate will ask again.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runShow(cmd, regenerate)
		},
	}
	cmd.Flags().BoolVar(&regenerate, "regenerate", false, "Replace the self-signed certificate now")
	return cmd
}

func runShow(cmd *cobra.Command, regenerate bool) error {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return err
	}
	opts := OptionsFromConfig(cfg)
	if regenerate && opts.Source() == SourceFile {
		return fmt.Errorf("gateway.tls.cert_file and key_file are set (%s); --regenerate applies only to the self-signed certificate", opts.CertFile)
	}
	m, err := Load(opts)
	if err != nil {
		return err
	}
	if regenerate {
		if err = m.Regenerate(); err != nil {
			return err
		}
		if _, werr := fmt.Fprintln(cmd.OutOrStdout(), "Self-signed certificate regenerated."); werr != nil {
			return werr
		}
	}
	info := m.Info()
	var b strings.Builder
	gw := cfg.Gateway
	if gw.HTTPSEnabled() {
		addrs := make([]string, 0, 2)
		for _, h := range gw.HTTPSBindHosts() {
			addrs = append(addrs, net.JoinHostPort(h, strconv.Itoa(gw.EffectiveTLSPort())))
		}
		fmt.Fprintf(&b, "HTTPS listener: %s (gateway.tls.mode %q)\n", strings.Join(addrs, ", "), gw.TLS.EffectiveMode())
	} else {
		b.WriteString("HTTPS listener: off (gateway.tls.mode \"off\"; set it to \"all\" or \"localhost\" to enable)\n")
	}
	fmt.Fprintf(&b, "Source:         %s\n", info.Source)
	fmt.Fprintf(&b, "Certificate:    %s\n", info.CertFile)
	fmt.Fprintf(&b, "Key:            %s\n", info.KeyFile)
	fmt.Fprintf(&b, "Subject:        %s\n", info.Subject)
	fmt.Fprintf(&b, "Names:          %s\n", strings.Join(info.Names(), ", "))
	left := time.Until(info.NotAfter)
	fmt.Fprintf(&b, "Not after:      %s (%d days)\n", info.NotAfter.Format(time.RFC3339), int(left.Hours()/24))
	fmt.Fprintf(&b, "SHA-256:        %s\n", info.Fingerprint)
	_, err = fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
}
