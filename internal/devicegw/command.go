// ClawEh
// License: MIT

// Package devicegw implements the `claw devices` CLI: provision + print a pairing
// QR for external OpenClaw-protocol devices (e.g. the Rabbit R1) and approve or
// reject pending device pairings. A running gateway picks up the config/token
// change automatically within a few seconds.
package devicegw

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/pkg/channels/device"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/gatewayproto"
)

// NewDevicesCommand returns the `claw devices` command group.
func NewDevicesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "Pair and manage external gateway devices (e.g. Rabbit R1)",
		Long: "Provision and print a pairing QR for external OpenClaw-protocol devices, and\n" +
			"approve or reject pending device pairings. The QR carries the gateway URL and a\n" +
			"shared token; scan it with the device. First connection creates a pending pairing\n" +
			"you approve here or in the WebUI.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newPairCommand(), newListCommand(), newApproveCommand(), newRejectCommand())
	return cmd
}

func openStore() (*device.Store, *config.Config, error) {
	cfg, err := config.LoadConfig(internal.GetConfigPath())
	if err != nil {
		return nil, nil, err
	}
	stateDir := filepath.Join(cfg.DataDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, nil, err
	}
	store, err := device.OpenStore(filepath.Join(stateDir, "gateway.db"))
	if err != nil {
		return nil, nil, err
	}
	return store, cfg, nil
}

func newPairCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "pair",
		Short: "Provision the device gateway and print a pairing QR code",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return pair() },
	}
}

func pair() error {
	configPath := internal.GetConfigPath()
	cfg, changed, err := device.EnsureProvisioned(configPath)
	if err != nil {
		return fmt.Errorf("provision device gateway: %w", err)
	}
	port := cfg.Gateway.Port
	if port == 0 {
		port = 18790
	}
	payload := gatewayproto.NewSetupPayload(device.LANIPv4s(), port, cfg.Channels.Device.Token, gatewayproto.SetupProtocolWS)
	encoded, err := payload.Encode()
	if err != nil {
		return fmt.Errorf("encode setup payload: %w", err)
	}
	ascii, err := device.RenderQRCodeASCII(encoded)
	if err != nil {
		return fmt.Errorf("render qr: %w", err)
	}

	fmt.Println("Scan this QR code with your device:")
	fmt.Println()
	fmt.Println(ascii)
	fmt.Println("Setup payload:", encoded)
	if len(payload.IPs) > 0 {
		fmt.Println("Connect URLs:")
		for _, ip := range payload.IPs {
			fmt.Printf("  ws://%s:%d\n", ip, port)
		}
	}
	if changed {
		fmt.Println("(generated a shared token and enabled the device gateway; a running gateway will pick this up within a few seconds)")
	}
	if device.IsLoopbackHost(cfg.Gateway.Host) {
		fmt.Printf("WARNING: gateway is bound to loopback (%s); LAN devices cannot connect. Set gateway.host to 0.0.0.0 or a LAN IP.\n", cfg.Gateway.Host)
	}
	if len(payload.IPs) == 0 {
		fmt.Println("WARNING: no routable LAN IPv4 address detected.")
	}
	fmt.Println("\nAfter the device connects, approve it with: claw devices approve <request-id>")
	return nil
}

func newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List pending and paired devices",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return list() },
	}
}

func list() error {
	store, _, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	pending, err := store.ListPending(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Pending (%d):\n", len(pending))
	for _, p := range pending {
		name := p.DisplayName
		if name == "" {
			name = p.ClientID
		}
		fmt.Printf("  %s  %s  device=%s  role=%s\n", p.RequestID, name, p.DeviceID, p.Role)
	}

	paired, err := store.ListPaired(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Paired (%d):\n", len(paired))
	for _, d := range paired {
		name := d.DisplayName
		if name == "" {
			name = d.ClientID
		}
		fmt.Printf("  %s  %s  roles=%v\n", d.DeviceID, name, d.Roles)
	}
	return nil
}

func newApproveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "approve <request-id>",
		Short: "Approve a pending device pairing",
		Args:  cobra.ExactArgs(1),
		RunE:  func(_ *cobra.Command, args []string) error { return approve(args[0]) },
	}
}

func approve(requestID string) error {
	store, _, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	dev, _, err := store.Approve(context.Background(), requestID, nil, nil)
	if err != nil {
		return err
	}
	fmt.Printf("Approved device %s\n", dev.DeviceID)
	return nil
}

func newRejectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reject <request-id>",
		Short: "Reject a pending device pairing",
		Args:  cobra.ExactArgs(1),
		RunE:  func(_ *cobra.Command, args []string) error { return reject(args[0]) },
	}
}

func reject(requestID string) error {
	store, _, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if err := store.Reject(context.Background(), requestID); err != nil {
		return err
	}
	fmt.Printf("Rejected pairing request %s\n", requestID)
	return nil
}
