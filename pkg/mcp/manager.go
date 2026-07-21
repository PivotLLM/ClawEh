package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// loadEnvFile loads environment variables from a file in .env format
// Each line should be in the format: KEY=value
// Lines starting with # are comments
// Empty lines are ignored
func loadEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open env file: %w", err)
	}
	defer file.Close()

	envVars := make(map[string]string)
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse KEY=value
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid format at line %d: %s", lineNum, line)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if key == "" {
			return nil, fmt.Errorf("invalid format at line %d: empty key", lineNum)
		}

		// Remove surrounding quotes if present
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		envVars[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading env file: %w", err)
	}

	return envVars, nil
}

// ServerConnection represents a connection to an MCP server
type ServerConnection struct {
	Name   string
	Client *client.Client
	Tools  []mcp.Tool
	// cfg is the resolved config this connection was established with. Sync
	// compares it against the reloaded config to decide whether a server actually
	// changed (reconnect) or was left untouched (keep the live process running).
	cfg config.MCPServerConfig
	// cmd is the child process for stdio transports (nil for sse/http). Held so a
	// disconnect can kill the whole process group, not just the direct child.
	cmd *exec.Cmd
	// probeStop closes to stop this connection's liveness-probe goroutine (nil when
	// probing is disabled). Closed exactly once by disconnect or Close.
	probeStop chan struct{}
}

// Manager manages multiple MCP server connections
type Manager struct {
	servers map[string]*ServerConnection
	mu      sync.RWMutex
	closed  atomic.Bool    // changed from bool to atomic.Bool to avoid TOCTOU race
	wg      sync.WaitGroup // tracks in-flight CallTool calls

	// Resilience tuning (see MCPConfig). Defaults set in NewManager; overridden
	// from config by applyTuning on load/sync.
	reconnectCooldown time.Duration
	callTimeout       time.Duration
	probeInterval     time.Duration // 0 disables liveness probing

	// cooldownUntil tracks, per server, the time before which a reconnect must not
	// be re-attempted after a failed reconnect. reconnecting tracks servers with a
	// reconnect in flight (for Status). Both guarded by cooldownMu.
	cooldownMu    sync.Mutex
	cooldownUntil map[string]time.Time
	reconnecting  map[string]bool
	probeWg       sync.WaitGroup // tracks liveness-probe goroutines

	// desired is the set of servers that should be connected (enabled, envFile
	// resolved), refreshed on every load/sync. RetryDisconnected uses it to
	// reconnect servers whose initial connect failed, without a restart.
	desiredMu sync.Mutex
	desired   map[string]config.MCPServerConfig
}

// Default resilience tuning, used when config leaves a value at 0.
const (
	defaultReconnectCooldown = 30 * time.Second
	defaultCallTimeout       = 5 * time.Minute
)

// NewManager creates a new MCP manager
func NewManager() *Manager {
	return &Manager{
		servers:           make(map[string]*ServerConnection),
		cooldownUntil:     make(map[string]time.Time),
		reconnecting:      make(map[string]bool),
		desired:           make(map[string]config.MCPServerConfig),
		reconnectCooldown: defaultReconnectCooldown,
		callTimeout:       defaultCallTimeout,
	}
}

// applyTuning updates the resilience knobs from config. A zero value keeps the
// existing default; probeInterval mirrors the config directly so a value of 0
// disables probing. Called before (re)connecting so new connections pick up the
// current settings.
func (m *Manager) applyTuning(mcpCfg config.MCPConfig) {
	if mcpCfg.ReconnectCooldownSeconds > 0 {
		m.reconnectCooldown = time.Duration(mcpCfg.ReconnectCooldownSeconds) * time.Second
	}
	if mcpCfg.CallTimeoutSeconds > 0 {
		m.callTimeout = time.Duration(mcpCfg.CallTimeoutSeconds) * time.Second
	}
	m.probeInterval = time.Duration(mcpCfg.LivenessProbeSeconds) * time.Second
}

// LoadFromConfig loads MCP servers from configuration
func (m *Manager) LoadFromConfig(ctx context.Context, cfg *config.Config) error {
	return m.LoadFromMCPConfig(ctx, cfg.Tools.MCP, cfg.WorkspacePath())
}

// LoadFromMCPConfig loads MCP servers from MCP configuration and workspace path.
// This is the minimal dependency version that doesn't require the full Config object.
func (m *Manager) LoadFromMCPConfig(
	ctx context.Context,
	mcpCfg config.MCPConfig,
	workspacePath string,
) error {
	m.applyTuning(mcpCfg)
	// Record the desired set up front so the background retry loop can recover any
	// server that fails its initial connect below, without a restart.
	m.setDesired(resolveDesired(mcpCfg, workspacePath))

	if len(mcpCfg.Servers) == 0 {
		logger.InfoCF("mcp", "No MCP servers configured", nil)
		return nil
	}

	logger.InfoCF("mcp", "Initializing MCP servers",
		map[string]any{
			"count": len(mcpCfg.Servers),
		})

	var wg sync.WaitGroup
	errs := make(chan error, len(mcpCfg.Servers))
	enabledCount := 0

	for name, serverCfg := range mcpCfg.Servers {
		if !serverCfg.Enabled {
			logger.DebugCF("mcp", "Skipping disabled server",
				map[string]any{
					"server": name,
				})
			continue
		}

		enabledCount++
		wg.Add(1)
		go func(name string, serverCfg config.MCPServerConfig, workspace string) {
			defer wg.Done()

			// Resolve relative envFile paths relative to workspace
			resolved, err := resolveServerEnvFile(name, serverCfg, workspace)
			if err != nil {
				logger.ErrorCF("mcp", "Invalid MCP server configuration",
					map[string]any{
						"server":   name,
						"env_file": serverCfg.EnvFile,
						"error":    err.Error(),
					})
				errs <- err
				return
			}
			serverCfg = resolved

			if err := m.ConnectServer(ctx, name, serverCfg); err != nil {
				logger.ErrorCF("mcp", "Failed to connect to MCP server",
					map[string]any{
						"server": name,
						"error":  err.Error(),
					})
				errs <- fmt.Errorf("failed to connect to server %s: %w", name, err)
			}
		}(name, serverCfg, workspacePath)
	}

	wg.Wait()
	close(errs)

	// Collect errors
	var allErrors []error
	for err := range errs {
		allErrors = append(allErrors, err)
	}

	connectedCount := len(m.GetServers())

	// If all enabled servers failed to connect, return aggregated error
	if enabledCount > 0 && connectedCount == 0 {
		logger.ErrorCF("mcp", "All MCP servers failed to connect",
			map[string]any{
				"failed": len(allErrors),
				"total":  enabledCount,
			})
		return errors.Join(allErrors...)
	}

	if len(allErrors) > 0 {
		logger.WarnCF("mcp", "Some MCP servers failed to connect",
			map[string]any{
				"failed":    len(allErrors),
				"connected": connectedCount,
				"total":     enabledCount,
			})
		// Don't fail completely if some servers successfully connected
	}

	logger.InfoCF("mcp", "MCP server initialization complete",
		map[string]any{
			"connected": connectedCount,
			"total":     enabledCount,
		})

	return nil
}

// resolveServerEnvFile returns a copy of cfg with a relative EnvFile made
// absolute against the workspace, so stdio servers load env identically
// regardless of process CWD. The input is not mutated.
func resolveServerEnvFile(
	name string,
	cfg config.MCPServerConfig,
	workspace string,
) (config.MCPServerConfig, error) {
	if cfg.EnvFile == "" || filepath.IsAbs(cfg.EnvFile) {
		return cfg, nil
	}
	if workspace == "" {
		return cfg, fmt.Errorf(
			"workspace path is empty while resolving relative envFile %q for server %s",
			cfg.EnvFile,
			name,
		)
	}
	cfg.EnvFile = filepath.Join(workspace, cfg.EnvFile)
	return cfg, nil
}

// buildStdioEnv assembles the child environment for a stdio server: the parent
// process environment, overlaid by an optional env file, overlaid by the
// server's explicit Env map (config wins over file wins over parent).
func buildStdioEnv(cfg config.MCPServerConfig) ([]string, error) {
	envMap := make(map[string]string)

	// Start with parent process environment
	for _, e := range os.Environ() {
		if idx := strings.Index(e, "="); idx > 0 {
			envMap[e[:idx]] = e[idx+1:]
		}
	}

	// Load environment variables from file if specified
	if cfg.EnvFile != "" {
		envVars, err := loadEnvFile(cfg.EnvFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load env file %s: %w", cfg.EnvFile, err)
		}
		for k, v := range envVars {
			envMap[k] = v
		}
	}

	// Environment variables from config override those from file
	for k, v := range cfg.Env {
		envMap[k] = v
	}

	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env, nil
}

// ConnectServer connects to a single MCP server
func (m *Manager) ConnectServer(
	ctx context.Context,
	name string,
	cfg config.MCPServerConfig,
) error {
	logger.InfoCF("mcp", "Connecting to MCP server",
		map[string]any{
			"server":     name,
			"command":    cfg.Command,
			"args_count": len(cfg.Args),
		})

	// Auto-detect transport when not set: a URL means streamable HTTP (the modern
	// remote default; a genuine SSE server must be typed "sse" explicitly), a
	// command means stdio.
	transportType := cfg.Type
	if transportType == "" {
		if cfg.URL != "" {
			transportType = "http"
		} else if cfg.Command != "" {
			transportType = "stdio"
		} else {
			return fmt.Errorf("either URL or command must be provided")
		}
	}

	var c *client.Client
	// stdioCmd is captured (via the stdio CommandFunc) so teardown can kill the
	// whole child process group; it stays nil for sse/http transports.
	var stdioCmd *exec.Cmd
	var err error

	switch transportType {
	case "http":
		if cfg.URL == "" {
			return fmt.Errorf("URL is required for http transport")
		}
		logger.DebugCF("mcp", "Using streamable HTTP transport",
			map[string]any{"server": name, "url": cfg.URL})
		var opts []transport.StreamableHTTPCOption
		if len(cfg.Headers) > 0 {
			opts = append(opts, transport.WithHTTPHeaders(cfg.Headers))
		}
		c, err = client.NewStreamableHttpClient(cfg.URL, opts...)
		if err != nil {
			return fmt.Errorf("failed to create HTTP client: %w", err)
		}
	case "sse":
		if cfg.URL == "" {
			return fmt.Errorf("URL is required for sse transport")
		}
		logger.DebugCF("mcp", "Using SSE transport",
			map[string]any{"server": name, "url": cfg.URL})
		var opts []transport.ClientOption
		if len(cfg.Headers) > 0 {
			opts = append(opts, client.WithHeaders(cfg.Headers))
		}
		c, err = client.NewSSEMCPClient(cfg.URL, opts...)
		if err != nil {
			return fmt.Errorf("failed to create SSE client: %w", err)
		}
	case "stdio":
		if cfg.Command == "" {
			return fmt.Errorf("command is required for stdio transport")
		}
		logger.DebugCF("mcp", "Using stdio transport",
			map[string]any{"server": name, "command": cfg.Command})
		env, envErr := buildStdioEnv(cfg)
		if envErr != nil {
			return envErr
		}
		// A custom CommandFunc lets us build the subprocess ourselves: own its
		// process group (so a teardown kills npx -> node -> browser as a unit) and
		// retain the *exec.Cmd for terminateStdioProcessTree. mark3labs' own Close
		// only signals the direct child, which would orphan chromium and hold the
		// profile lock.
		cmdFunc := func(ctx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
			cmd := exec.CommandContext(ctx, command, args...)
			cmd.Env = env
			prepareStdioCommand(cmd)
			stdioCmd = cmd
			return cmd, nil
		}
		st := transport.NewStdioWithOptions(cfg.Command, env, cfg.Args, transport.WithCommandFunc(cmdFunc))
		c = client.NewClient(st)
	default:
		return fmt.Errorf(
			"unsupported transport type: %s (supported: stdio, http, sse)",
			transportType,
		)
	}

	// Start the transport (spawns the stdio subprocess / opens the SSE stream),
	// then run the MCP initialize handshake. On any failure, close the client and
	// reap any stdio child so a failed connect leaves nothing running.
	if err = c.Start(ctx); err != nil {
		_ = c.Close()
		terminateStdioProcessTree(stdioCmd)
		return fmt.Errorf("failed to start transport: %w", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ClientInfo = mcp.Implementation{Name: "claw", Version: "1.0.0"}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.Capabilities = mcp.ClientCapabilities{}

	initResult, err := c.Initialize(ctx, initReq)
	if err != nil {
		_ = c.Close()
		terminateStdioProcessTree(stdioCmd)
		return fmt.Errorf("failed to connect: %w", err)
	}
	logger.InfoCF("mcp", "Connected to MCP server",
		map[string]any{
			"server":        name,
			"serverName":    initResult.ServerInfo.Name,
			"serverVersion": initResult.ServerInfo.Version,
			"protocol":      initResult.ProtocolVersion,
		})

	// List available tools. A listing failure is non-fatal: the server is
	// connected, it simply exposes no callable tools to us this session.
	var tools []mcp.Tool
	toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		logger.WarnCF("mcp", "Failed to list tools from MCP server",
			map[string]any{"server": name, "error": err.Error()})
	} else {
		tools = toolsResult.Tools
		logger.InfoCF("mcp", "Listed tools from MCP server",
			map[string]any{"server": name, "toolCount": len(tools)})
	}

	// Store connection. Guard against a concurrent Close so a reconnect racing
	// shutdown can't resurrect a server on a closed manager (and leak a probe).
	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		_ = c.Close()
		terminateStdioProcessTree(stdioCmd)
		return fmt.Errorf("manager is closed")
	}
	conn := &ServerConnection{
		Name:   name,
		Client: c,
		Tools:  tools,
		cfg:    cfg,
		cmd:    stdioCmd,
	}
	if m.probeInterval > 0 {
		conn.probeStop = m.startProbe(name)
	}
	m.servers[name] = conn
	m.mu.Unlock()

	return nil
}

// RevealTogether reports whether this server's tools should reveal as a group
// under progressive discovery (from its config).
func (c *ServerConnection) RevealTogether() bool { return c.cfg.RevealTogether }

// GetServers returns all connected servers
func (m *Manager) GetServers() map[string]*ServerConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*ServerConnection, len(m.servers))
	for k, v := range m.servers {
		result[k] = v
	}
	return result
}

// GetServer returns a specific server connection
func (m *Manager) GetServer(name string) (*ServerConnection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	conn, ok := m.servers[name]
	return conn, ok
}

// disconnect closes a single server connection and removes it from the manager,
// killing the child process group for stdio servers so no grandchild survives to
// hold a resource lock. Callers must not hold m.mu.
func (m *Manager) disconnect(name string) {
	m.mu.Lock()
	conn, ok := m.servers[name]
	if ok {
		delete(m.servers, name)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	if conn.probeStop != nil {
		close(conn.probeStop)
	}
	if err := conn.Client.Close(); err != nil {
		logger.WarnCF("mcp", "Failed to close MCP server connection",
			map[string]any{"server": name, "error": err.Error()})
	}
	terminateStdioProcessTree(conn.cmd)
}

// resolveDesired computes the set of servers that should be connected: enabled
// servers with their envFile resolved the same way the initial load resolves it,
// so change-detection and reconnection compare like with like. Servers with an
// invalid config are logged and skipped (they can never connect).
func resolveDesired(mcpCfg config.MCPConfig, workspacePath string) map[string]config.MCPServerConfig {
	desired := make(map[string]config.MCPServerConfig, len(mcpCfg.Servers))
	for name, serverCfg := range mcpCfg.Servers {
		if !serverCfg.Enabled {
			continue
		}
		resolved, err := resolveServerEnvFile(name, serverCfg, workspacePath)
		if err != nil {
			logger.ErrorCF("mcp", "Invalid MCP server configuration",
				map[string]any{"server": name, "error": err.Error()})
			continue
		}
		desired[name] = resolved
	}
	return desired
}

// setDesired records the current desired-server set for RetryDisconnected.
func (m *Manager) setDesired(desired map[string]config.MCPServerConfig) {
	m.desiredMu.Lock()
	m.desired = desired
	m.desiredMu.Unlock()
}

// RetryDisconnected attempts to connect any desired (enabled) server that is not
// currently connected and not in reconnect cooldown, returning the names that
// newly connected so the caller can register their tools. It exists so a server
// whose *initial* connect failed (upstream briefly down, or a config saved
// mid-edit) recovers automatically without a restart — the initial-connect
// analogue of the probe-driven reconnect that already covers drop-after-connect.
func (m *Manager) RetryDisconnected(ctx context.Context) []string {
	if m.closed.Load() {
		return nil
	}
	m.desiredMu.Lock()
	desired := make(map[string]config.MCPServerConfig, len(m.desired))
	for k, v := range m.desired {
		desired[k] = v
	}
	m.desiredMu.Unlock()

	var connected []string
	for name, serverCfg := range desired {
		if _, ok := m.GetServer(name); ok {
			continue // already connected
		}
		if _, cooling := m.reconnectCooldownUntil(name); cooling {
			continue // still in post-failure cooldown
		}
		m.setReconnecting(name, true)
		cctx, cancel := context.WithTimeout(ctx, m.callTimeout)
		err := m.ConnectServer(cctx, name, serverCfg)
		cancel()
		m.setReconnecting(name, false)
		if err != nil {
			m.markReconnectFailed(name)
			logger.WarnCF("mcp", "MCP background connect failed; server in cooldown",
				map[string]any{
					"server":         name,
					"error":          err.Error(),
					"cooldown_until": m.now().Add(m.reconnectCooldown).Format(time.RFC3339),
				})
			continue
		}
		m.clearReconnectCooldown(name)
		logger.InfoCF("mcp", "MCP server connected on retry", map[string]any{"server": name})
		connected = append(connected, name)
	}
	return connected
}

// Sync reconciles live connections to match mcpCfg without disturbing servers
// whose configuration is unchanged. On config reload this keeps long-lived stdio
// servers (e.g. a playwright browser holding a persistent profile) running rather
// than tearing every server down and relaunching it — the relaunch races the old
// child's resource locks ("Browser is already in use"). Servers that were removed,
// disabled, or reconfigured are disconnected; new or changed servers are
// (re)connected. Returns the joined errors of any failed reconnects.
func (m *Manager) Sync(
	ctx context.Context,
	mcpCfg config.MCPConfig,
	workspacePath string,
) error {
	m.applyTuning(mcpCfg)

	// Desired = enabled servers, envFile resolved the same way the initial load
	// resolves it so change-detection compares like with like.
	desired := resolveDesired(mcpCfg, workspacePath)
	m.setDesired(desired)

	// Drop connections that are gone, disabled, or reconfigured.
	for name, conn := range m.GetServers() {
		want, keep := desired[name]
		if keep && reflect.DeepEqual(conn.cfg, want) {
			continue
		}
		reason := "config-changed"
		if !keep {
			reason = "removed-or-disabled"
		}
		logger.InfoCF("mcp", "Reconciling MCP server (disconnect)",
			map[string]any{"server": name, "reason": reason})
		m.disconnect(name)
	}

	// (Re)connect servers that are newly desired or were just dropped for a change.
	var errs []error
	for name, serverCfg := range desired {
		if _, ok := m.GetServer(name); ok {
			continue // unchanged and still connected
		}
		if err := m.ConnectServer(ctx, name, serverCfg); err != nil {
			logger.ErrorCF("mcp", "Failed to connect to MCP server",
				map[string]any{"server": name, "error": err.Error()})
			errs = append(errs, fmt.Errorf("server %s: %w", name, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// CallTool calls a tool on a specific server
func (m *Manager) CallTool(
	ctx context.Context,
	serverName, toolName string,
	arguments map[string]any,
) (*mcp.CallToolResult, error) {
	// Check if closed before acquiring lock (fast path)
	if m.closed.Load() {
		return nil, fmt.Errorf("manager is closed")
	}

	m.mu.RLock()
	// Double-check after acquiring lock to prevent TOCTOU race
	if m.closed.Load() {
		m.mu.RUnlock()
		return nil, fmt.Errorf("manager is closed")
	}
	conn, ok := m.servers[serverName]
	if ok {
		m.wg.Add(1) // Add to WaitGroup while holding the lock
	}
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("server %s not found", serverName)
	}
	defer m.wg.Done()

	// Backstop: if the caller supplied no deadline, cap the call so a hung server
	// cannot block forever. A caller-provided deadline is always honored as-is.
	callCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && m.callTimeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, m.callTimeout)
		defer cancel()
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = arguments

	result, err := conn.Client.CallTool(callCtx, req)
	if err == nil {
		return result, nil
	}

	// Only a connection-level failure (the session dropped) is retried — a normal
	// tool error is returned as-is so a side-effecting call is never re-run after
	// the server may already have processed it.
	if !isConnectionError(err) {
		return nil, fmt.Errorf("failed to call tool: %w", err)
	}

	logger.WarnCF("mcp", "MCP tool call failed on a dropped connection; reconnecting and retrying once",
		map[string]any{"server": serverName, "tool": toolName, "error": err.Error()})

	if rerr := m.reconnect(callCtx, serverName, conn.cfg); rerr != nil {
		return nil, fmt.Errorf("failed to call tool (reconnect failed): %w", errors.Join(err, rerr))
	}

	m.mu.RLock()
	newConn, ok := m.servers[serverName]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("failed to call tool: server %s unavailable after reconnect", serverName)
	}

	result, err = newConn.Client.CallTool(callCtx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to call tool (after reconnect): %w", err)
	}
	return result, nil
}

// Close closes all server connections
func (m *Manager) Close() error {
	// Use Swap to atomically set closed=true and get the previous value
	// This prevents TOCTOU race with CallTool's closed check
	if m.closed.Swap(true) {
		return nil // already closed
	}

	// Wait for all in-flight CallTool calls to finish before closing sessions
	// After closed=true is set, no new CallTool can start (they check closed first)
	m.wg.Wait()

	// Signal every liveness-probe goroutine to stop, then wait for them to exit
	// before tearing down clients. A probe mid-reconnect sees closed==true and bails
	// (ConnectServer/reconnect both check it), so no server is resurrected here.
	m.mu.Lock()
	for _, conn := range m.servers {
		if conn.probeStop != nil {
			close(conn.probeStop)
			conn.probeStop = nil
		}
	}
	m.mu.Unlock()
	m.probeWg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	logger.InfoCF("mcp", "Closing all MCP server connections",
		map[string]any{
			"count": len(m.servers),
		})

	var errs []error
	for name, conn := range m.servers {
		if err := conn.Client.Close(); err != nil {
			logger.ErrorCF("mcp", "Failed to close server connection",
				map[string]any{
					"server": name,
					"error":  err.Error(),
				})
			errs = append(errs, fmt.Errorf("server %s: %w", name, err))
		}
		// Safety net: kill any stdio grandchildren (e.g. chromium) the transport's
		// direct-child shutdown leaves orphaned, so no profile lock survives.
		terminateStdioProcessTree(conn.cmd)
	}

	m.servers = make(map[string]*ServerConnection)

	if len(errs) > 0 {
		return fmt.Errorf("failed to close %d server(s): %w", len(errs), errors.Join(errs...))
	}

	return nil
}

// GetAllTools returns all tools from all connected servers
func (m *Manager) GetAllTools() map[string][]mcp.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string][]mcp.Tool)
	for name, conn := range m.servers {
		if len(conn.Tools) > 0 {
			result[name] = conn.Tools
		}
	}
	return result
}
