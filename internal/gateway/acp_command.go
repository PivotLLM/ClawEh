package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	acplib "github.com/a3tai/openclaw-go/acp"
	"github.com/a3tai/openclaw-go/gateway"
	"github.com/a3tai/openclaw-go/identity"
	"github.com/a3tai/openclaw-go/protocol"
	"github.com/spf13/cobra"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/channels/device"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

// NewACPCommand builds the `claw acp` subcommand: an Agent Client Protocol (ACP)
// agent that speaks JSON-RPC 2.0 over stdio and bridges to the ALREADY-RUNNING
// claw over its localhost WebSocket (the device listener on 127.0.0.1:<port>).
// It is intended to be spawned by an ACP client (e.g. rabbit-agent for the Rabbit
// R1). The bridge holds no agent loop of its own — the one running claw does
// the work, so there is exactly one ClawEh instance.
func NewACPCommand() *cobra.Command {
	var debug bool
	var url string
	var noAutoPair bool
	cmd := &cobra.Command{
		Use:   "acp",
		Short: "Serve the Agent Client Protocol (ACP) over stdio, bridging to the local " + app.Name(),
		Long: "Serve the Agent Client Protocol over stdin/stdout for an ACP client (such as\n" +
			"rabbit-agent for the Rabbit R1) and forward prompts to the already-running\n" +
			"" + app.Name() + " over its localhost WebSocket. The client spawns this process and\n" +
			"exchanges JSON-RPC 2.0 messages on the pipe; stdin EOF shuts it down.\n\n" +
			"The bridge authenticates to " + app.Name() + " as a paired device (Ed25519 identity\n" +
			"+ the configured device token). Because it is a local same-user process, it\n" +
			"auto-approves its own pairing in the local store on first connect (disable\n" +
			"with --no-auto-pair to require a manual `claw devices approve`).\n\n" +
			"stdout is the protocol wire, so all logging goes to the log file only.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return acpCmd(debug, url, !noAutoPair)
		},
	}
	cmd.Flags().BoolVarP(&debug, "debug", "d", false, "Enable debug logging (written to the log file, never stdout)")
	cmd.Flags().StringVar(&url, "url", "", "Device listener WebSocket URL (default ws://127.0.0.1:<device-port>/, wss:// with channels.device.tls)")
	cmd.Flags().BoolVar(&noAutoPair, "no-auto-pair", false, "Require manual pairing approval instead of self-approving the local bridge")
	return cmd
}

func acpCmd(debug bool, wsURL string, autoPair bool) error {
	// stdout is the ACP JSON-RPC wire — NOTHING else may write to it. Silence the
	// console logger before any log line and route everything to the log file.
	logger.DisableConsole()

	baseDir := internal.GetClawHome()
	logPath := filepath.Join(baseDir, "logs", "claw.log")
	logger.SetErrorLogLevel(logger.ParseLevel(global.ErrorLogLevel))
	if err := logger.EnableFileLogging(logPath, false); err != nil {
		fmt.Fprintf(os.Stderr, "acp: enable file logging: %v\n", err)
	}
	installSpawnllmLogging()

	cfg, err := internal.LoadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}
	if debug {
		logger.SetLevel(logger.DEBUG)
	} else if cfg.Logging.Level != "" {
		logger.SetLevel(logger.ParseLevel(cfg.Logging.Level))
	}
	logger.DisableConsole() // re-assert after config
	logger.SetLogMessageContent(cfg.Logging.LogMessageContent)

	dev := cfg.Channels.Device
	// Shared auth secret the device listener accepts (long token preferred, else
	// the typeable word passphrase). On loopback with neither set, auth is open.
	authToken := dev.Token
	if authToken == "" {
		authToken = dev.WordToken
	}
	if wsURL == "" {
		wsURL = defaultDeviceWSURL(dev.Host, dev.Port, dev.TLS)
	}
	var tlsConfig *tls.Config
	if strings.HasPrefix(wsURL, "wss://") {
		tlsConfig = pinnedGatewayTLS(tlscert.OptionsFromConfig(cfg))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The event callback is registered at client-construction time but needs the
	// bridge, and the bridge needs the client — close over a late-bound pointer.
	var br *acpBridge
	conn, err := connectBridge(ctx, cfg.DataDir(), wsURL, authToken, tlsConfig, autoPair, func(ev protocol.Event) {
		if br != nil {
			br.handleGatewayEvent(ev)
		}
	})
	if err != nil {
		return err
	}
	client := conn.client
	defer utils.CloseQuietly(client)

	br = newACPBridge(&abortSessionKeyClient{Client: client})
	server := acplib.NewServer(br, os.Stdin, os.Stdout)
	br.setNotifier(server)

	logger.InfoC("acp", "ACP stdio server ready (bridged to claw)")
	srvInfo := ""
	if hello := client.Hello(); hello != nil {
		srvInfo = fmt.Sprintf(" (server %s, protocol %d)", hello.Server.Version, hello.Protocol)
	}
	fmt.Fprintf(os.Stderr, "claw acp: connected%s — ACP ready, reading JSON-RPC on stdin (Ctrl-D to exit)\n", srvInfo)

	// Serve blocks until stdin closes (client disconnect), the gateway drops, or
	// the context is cancelled.
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ctx) }()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("acp serve: %w", err)
		}
		logger.InfoC("acp", "ACP stdio client disconnected; shutting down")
	case <-client.Done():
		logger.WarnC("acp", "Connection to claw closed; shutting down")
	}
	return nil
}

// bridgeConn is the bridge's live, paired connection to the device listener.
type bridgeConn struct {
	client   *gateway.Client
	identity *identity.Identity
	// selfApproved reports that this connect found the device unpaired and
	// approved its own pairing request.
	selfApproved bool
}

// connectBridge connects the bridge to the device listener at wsURL as a paired
// node. Its Ed25519 identity and issued device token persist under
// <dataDir>/state/acp-bridge, so pairing happens once across the short-lived
// spawns an ACP client makes. authToken is the shared device token (empty for
// open loopback auth); tlsConfig is nil for ws://. With autoPair, a not-paired
// rejection is answered by approving this device's own pending request in
// <dataDir>/state/gateway.db and reconnecting. onEvent receives gateway events.
func connectBridge(ctx context.Context, dataDir, wsURL, authToken string, tlsConfig *tls.Config, autoPair bool, onEvent func(protocol.Event)) (*bridgeConn, error) {
	// Persisted Ed25519 device identity + issued device token (survives across the
	// short-lived spawns rabbit-agent makes, so pairing happens only once).
	idDir := filepath.Join(dataDir, "state", "acp-bridge")
	if mkErr := os.MkdirAll(idDir, 0o700); mkErr != nil {
		return nil, fmt.Errorf("acp: create identity dir: %w", mkErr)
	}
	idStore, err := identity.NewStore(idDir)
	if err != nil {
		return nil, fmt.Errorf("acp: open identity store: %w", err)
	}
	id, err := idStore.LoadOrGenerate()
	if err != nil {
		return nil, fmt.Errorf("acp: load device identity: %w", err)
	}
	deviceToken := idStore.LoadDeviceToken()

	logger.InfoCF("acp", "Starting ACP↔claw bridge", map[string]any{
		"app": app.Name(), "version": app.Version(), "url": wsURL, "deviceId": id.DeviceID,
	})
	// Human-facing progress goes to stderr — stdout is the ACP protocol wire, so it
	// must stay clean. ACP clients (rabbit-agent) read stdout only and ignore this.
	fmt.Fprintf(os.Stderr, "claw acp: connecting to %s (device %s)…\n", wsURL, id.DeviceID)

	// The client sends one token. A stored device token is tried on its own: the
	// options apply in order, so adding the shared token as well would override
	// it, and a shared-token connect makes the gateway rotate the device's tokens
	// on every launch. The shared token is sent only when there is no device
	// token to try.
	makeClient := func(tok string) *gateway.Client {
		opts := []gateway.Option{gateway.WithIdentity(id, tok)}
		if tok == "" {
			opts = append(opts, gateway.WithToken(authToken))
		}
		opts = append(opts,
			gateway.WithRole(protocol.RoleNode),
			gateway.WithClientInfo(protocol.ClientInfo{
				ID: protocol.ClientIDGateway,
				// Protocol handshake: bare semver (see global.GetVersion).
				Version:  app.SemVer(),
				Platform: "go",
				Mode:     protocol.ClientModeNode,
			}),
			gateway.WithOnEvent(onEvent),
		)
		if tlsConfig != nil {
			opts = append(opts, gateway.WithTLSConfig(tlsConfig))
		}
		return gateway.NewClient(opts...)
	}

	tryConnect := func(tok string) (*gateway.Client, error) {
		c := makeClient(tok)
		if err := c.Connect(ctx, wsURL); err != nil {
			utils.CloseQuietly(c)
			return nil, err
		}
		return c, nil
	}

	// Connect ladder, resilient to a stale cached device token (which happens after
	// the paired device is removed — RemovePaired revokes the token, so the cached
	// one no longer authenticates and the failure is an auth error, NOT NOT_PAIRED):
	//   1. try the cached device token, if any;
	//   2. on any failure, drop the cached token and retry with the shared token
	//      (a dead token must be cleared so the shared token is sent and re-pairs);
	//   3. if that says NOT_PAIRED, self-approve our own pending pairing and reconnect.
	var client *gateway.Client
	var connErr error
	selfApproved := false
	if deviceToken != "" {
		client, connErr = tryConnect(deviceToken)
		if connErr != nil {
			logger.WarnCF("acp", "connect with cached device token failed; clearing it and retrying with the shared token",
				map[string]any{"deviceId": id.DeviceID, "error": connErr.Error()})
			if clearErr := idStore.ClearDeviceToken(); clearErr != nil {
				logger.WarnCF("acp", "failed to clear cached device token", map[string]any{"error": clearErr.Error()})
			}
			deviceToken = ""
		}
	}
	if client == nil {
		client, connErr = tryConnect("")
	}
	// The bridge is a local same-user process, so on NOT_PAIRED it self-approves its
	// own pending pairing directly in the (WAL-mode, concurrent-safe) store — the
	// gateway records the pending request before rejecting, then re-reads paired
	// state on the reconnect. This does not touch the network/proxy device path.
	if connErr != nil && isPairingError(connErr) && autoPair {
		logger.WarnCF("acp", "Device not paired; auto-approving local bridge", map[string]any{"deviceId": id.DeviceID})
		tok, perr := autoApproveLocalDevice(ctx, dataDir, id.DeviceID)
		if perr != nil {
			return nil, fmt.Errorf("acp: auto-pair failed for device %s (approve manually with `claw devices`): %w", id.DeviceID, perr)
		}
		if tok != "" {
			deviceToken = tok
			if serr := idStore.SaveDeviceToken(tok); serr != nil {
				logger.WarnCF("acp", "Failed to persist device token", map[string]any{"error": serr.Error()})
			}
		}
		client, connErr = tryConnect(deviceToken)
		selfApproved = true
	}
	if connErr != nil {
		if isPairingError(connErr) {
			return nil, fmt.Errorf("this device (%s) is not paired yet — approve it once with `claw devices`, then re-run: %w", id.DeviceID, connErr)
		}
		return nil, fmt.Errorf("acp: connect to %s: %w", wsURL, connErr)
	}

	// Persist a freshly issued device token so future spawns skip the shared secret.
	if hello := client.Hello(); hello != nil && hello.Auth != nil && hello.Auth.DeviceToken != "" && hello.Auth.DeviceToken != deviceToken {
		if err := idStore.SaveDeviceToken(hello.Auth.DeviceToken); err != nil {
			logger.WarnCF("acp", "Failed to persist issued device token", map[string]any{"error": err.Error()})
		}
	}
	return &bridgeConn{client: client, identity: id, selfApproved: selfApproved}, nil
}

// abortSessionKeyClient adapts *gateway.Client to gatewaySender, defaulting the
// sessionKey on chat.abort (the bridge always uses the node's "main" session).
type abortSessionKeyClient struct{ *gateway.Client }

func (c *abortSessionKeyClient) ChatAbort(ctx context.Context, params protocol.ChatAbortParams) error {
	if params.SessionKey == "" {
		params.SessionKey = "main"
	}
	return c.Client.ChatAbort(ctx, params)
}

// autoApproveLocalDevice approves this bridge's own pending pairing by writing
// directly to the device store (SQLite WAL — safe to open concurrently with the
// running gateway). It returns the issued device token, if any. Safe because the
// bridge is a local same-user process that already owns the data dir; it only
// approves its OWN device id, never another device's pending request.
func autoApproveLocalDevice(ctx context.Context, dataDir, deviceID string) (string, error) {
	store, err := device.OpenStore(ctx, filepath.Join(dataDir, "state", "gateway.db"))
	if err != nil {
		return "", fmt.Errorf("open device store: %w", err)
	}
	defer utils.CloseQuietly(store)

	pending, err := store.ListPending(ctx)
	if err != nil {
		return "", fmt.Errorf("list pending pairings: %w", err)
	}
	requestID := ""
	for _, p := range pending {
		if p.DeviceID == deviceID {
			requestID = p.RequestID
			break
		}
	}
	if requestID == "" {
		return "", fmt.Errorf("no pending pairing found for device %s", deviceID)
	}
	_, tokens, err := store.Approve(ctx, requestID, nil, nil)
	if err != nil {
		return "", fmt.Errorf("approve pairing %s: %w", requestID, err)
	}
	if len(tokens) > 0 {
		return tokens[0].Token, nil
	}
	return "", nil
}

// defaultDeviceWSURL builds the loopback WebSocket URL for the device listener,
// wss:// when channels.device.tls is on. A 0.0.0.0 bind is dialed on
// 127.0.0.1 (the bridge is always local).
func defaultDeviceWSURL(host string, port int, useTLS bool) string {
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if port == 0 {
		port = device.DefaultDevicePort
	}
	scheme := "ws"
	if useTLS {
		scheme = "wss"
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/"
}

// pinnedGatewayTLS trusts exactly the certificate the gateway serves, read
// from its file: the bridge dials 127.0.0.1, which a self-signed or
// user-supplied certificate need not name, so the chain and name checks are
// replaced by a fingerprint match against the file on this machine.
func pinnedGatewayTLS(opts tlscert.Options) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // verified by VerifyConnection against the local certificate file
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("acp: the device listener presented no certificate")
			}
			want, err := tlscert.InspectFile(opts)
			if err != nil {
				return fmt.Errorf("acp: read the TLS certificate: %w", err)
			}
			if got := tlscert.Fingerprint(cs.PeerCertificates[0]); got != want.Fingerprint {
				return fmt.Errorf("acp: certificate %s does not match %s", got, want.CertFile)
			}
			return nil
		},
	}
}

// isPairingError reports whether a connect error is the gateway's not-paired
// rejection (so we can print approve instructions instead of a raw error).
func isPairingError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "NOT_PAIRED") || strings.Contains(msg, "PAIR")
}
