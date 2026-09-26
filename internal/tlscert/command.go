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
		Long: "The gateway serves plain HTTP on loopback only. When gateway.host is not a loopback\n" +
			"address it also serves HTTPS on gateway.host:gateway.tls_port (default 18443) with\n" +
			"either the operator's certificate (gateway.tls.cert_file and key_file) or a\n" +
			"self-signed one it generates under <CLAW_HOME>/tls and renews itself.\n\n" +
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
		fmt.Fprintf(&b, "HTTPS listener: %s (gateway.host %q)\n", net.JoinHostPort(gw.Host, strconv.Itoa(gw.EffectiveTLSPort())), gw.Host)
	} else {
		fmt.Fprintf(&b, "HTTPS listener: off (gateway.host %q is loopback; set it to a LAN address or 0.0.0.0 to enable)\n", gw.Host)
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
