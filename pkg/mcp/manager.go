package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// headerTransport is an http.RoundTripper that adds custom headers to requests
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid modifying the original
	req = req.Clone(req.Context())

	// Add custom headers
	for key, value := range t.headers {
		req.Header.Set(key, value)
	}

	// Use the base transport
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

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
	Name    string
	Client  *mcp.Client
	Session *mcp.ClientSession
	Tools   []*mcp.Tool
	// cfg is the resolved config this connection was established with. Sync
	// compares it against the reloaded config to decide whether a server actually
	// changed (reconnect) or was left untouched (keep the live process running).
	cfg config.MCPServerConfig
	// cmd is the child process for stdio transports (nil for sse/http). Held so a
	// disconnect can kill the whole process group, not just the direct child.
	cmd *exec.Cmd
}

// Manager manages multiple MCP server connections
type Manager struct {
	servers map[string]*ServerConnection
	mu      sync.RWMutex
	closed  atomic.Bool    // changed from bool to atomic.Bool to avoid TOCTOU race
	wg      sync.WaitGroup // tracks in-flight CallTool calls
}

// NewManager creates a new MCP manager
func NewManager() *Manager {
	return &Manager{
		servers: make(map[string]*ServerConnection),
	}
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

	// Create client
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "claw",
		Version: "1.0.0",
	}, nil)

	// Create transport based on configuration
	// Auto-detect transport type if not explicitly specified
	var transport mcp.Transport
	// stdioCmd is retained for stdio servers so the connection can later kill the
	// whole child process group; it stays nil for sse/http transports.
	var stdioCmd *exec.Cmd
	transportType := cfg.Type

	// Auto-detect: if URL is provided, use SSE; if command is provided, use stdio
	if transportType == "" {
		if cfg.URL != "" {
			transportType = "sse"
		} else if cfg.Command != "" {
			transportType = "stdio"
		} else {
			return fmt.Errorf("either URL or command must be provided")
		}
	}

	switch transportType {
	case "sse", "http":
		if cfg.URL == "" {
			return fmt.Errorf("URL is required for SSE/HTTP transport")
		}
		logger.DebugCF("mcp", "Using SSE/HTTP transport",
			map[string]any{
				"server": name,
				"url":    cfg.URL,
			})

		sseTransport := &mcp.StreamableClientTransport{
			Endpoint: cfg.URL,
		}

		// Add custom headers if provided
		if len(cfg.Headers) > 0 {
			// Create a custom HTTP client with header-injecting transport
			sseTransport.HTTPClient = &http.Client{
				Transport: &headerTransport{
					base:    http.DefaultTransport,
					headers: cfg.Headers,
				},
			}
			logger.DebugCF("mcp", "Added custom HTTP headers",
				map[string]any{
					"server":       name,
					"header_count": len(cfg.Headers),
				})
		}

		transport = sseTransport
	case "stdio":
		if cfg.Command == "" {
			return fmt.Errorf("command is required for stdio transport")
		}
		logger.DebugCF("mcp", "Using stdio transport",
			map[string]any{
				"server":  name,
				"command": cfg.Command,
			})
		// Create command with context
		cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)

		// Build environment variables with proper override semantics
		// Use a map to ensure config variables override file variables
		envMap := make(map[string]string)

		// Start with parent process environment
		for _, e := range cmd.Environ() {
			if idx := strings.Index(e, "="); idx > 0 {
				envMap[e[:idx]] = e[idx+1:]
			}
		}

		// Load environment variables from file if specified
		if cfg.EnvFile != "" {
			envVars, err := loadEnvFile(cfg.EnvFile)
			if err != nil {
				return fmt.Errorf("failed to load env file %s: %w", cfg.EnvFile, err)
			}
			for k, v := range envVars {
				envMap[k] = v
			}
			logger.DebugCF("mcp", "Loaded environment variables from file",
				map[string]any{
					"server":    name,
					"envFile":   cfg.EnvFile,
					"var_count": len(envVars),
				})
		}

		// Environment variables from config override those from file
		for k, v := range cfg.Env {
			envMap[k] = v
		}

		// Convert map to slice
		env := make([]string, 0, len(envMap))
		for k, v := range envMap {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = env

		// Own process group so a teardown can kill npx -> node -> browser as a unit.
		prepareStdioCommand(cmd)

		stdioCmd = cmd
		transport = &mcp.CommandTransport{Command: cmd}
	default:
		return fmt.Errorf(
			"unsupported transport type: %s (supported: stdio, sse, http)",
			transportType,
		)
	}

	// Connect to server
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	// Get server info
	initResult := session.InitializeResult()
	logger.InfoCF("mcp", "Connected to MCP server",
		map[string]any{
			"server":        name,
			"serverName":    initResult.ServerInfo.Name,
			"serverVersion": initResult.ServerInfo.Version,
			"protocol":      initResult.ProtocolVersion,
		})

	// List available tools if supported
	var tools []*mcp.Tool
	if initResult.Capabilities.Tools != nil {
		for tool, err := range session.Tools(ctx, nil) {
			if err != nil {
				logger.WarnCF("mcp", "Error listing tool",
					map[string]any{
						"server": name,
						"error":  err.Error(),
					})
				continue
			}
			tools = append(tools, tool)
		}

		logger.InfoCF("mcp", "Listed tools from MCP server",
			map[string]any{
				"server":    name,
				"toolCount": len(tools),
			})
	}

	// Store connection
	m.mu.Lock()
	m.servers[name] = &ServerConnection{
		Name:    name,
		Client:  client,
		Session: session,
		Tools:   tools,
		cfg:     cfg,
		cmd:     stdioCmd,
	}
	m.mu.Unlock()

	return nil
}

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
	if err := conn.Session.Close(); err != nil {
		logger.WarnCF("mcp", "Failed to close MCP server connection",
			map[string]any{"server": name, "error": err.Error()})
	}
	terminateStdioProcessTree(conn.cmd)
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
	// Desired = enabled servers, envFile resolved the same way the initial load
	// resolves it so change-detection compares like with like.
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

	params := &mcp.CallToolParams{
		Name:      toolName,
		Arguments: arguments,
	}

	result, err := conn.Session.CallTool(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("failed to call tool: %w", err)
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

	m.mu.Lock()
	defer m.mu.Unlock()

	logger.InfoCF("mcp", "Closing all MCP server connections",
		map[string]any{
			"count": len(m.servers),
		})

	var errs []error
	for name, conn := range m.servers {
		if err := conn.Session.Close(); err != nil {
			logger.ErrorCF("mcp", "Failed to close server connection",
				map[string]any{
					"server": name,
					"error":  err.Error(),
				})
			errs = append(errs, fmt.Errorf("server %s: %w", name, err))
		}
		// Safety net: kill any stdio grandchildren (e.g. chromium) the SDK's
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
func (m *Manager) GetAllTools() map[string][]*mcp.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string][]*mcp.Tool)
	for name, conn := range m.servers {
		if len(conn.Tools) > 0 {
			result[name] = conn.Tools
		}
	}
	return result
}
