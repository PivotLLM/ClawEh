package mcp

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// probeTimeoutCap bounds a single liveness ping regardless of the probe interval,
// so a stalled server is declared dead promptly rather than after the full interval.
const probeTimeoutCap = 10 * time.Second

// Connection-state values reported by Status.
const (
	StateConnected    = "connected"
	StateReconnecting = "reconnecting"
	StateCooldown     = "cooldown"
)

// ServerStatus is a point-in-time snapshot of one server's connection health,
// suitable for a status API. CooldownUntil is zero unless State is cooldown.
type ServerStatus struct {
	Name          string    `json:"name"`
	State         string    `json:"state"`
	Transport     string    `json:"transport,omitempty"`
	ToolCount     int       `json:"tool_count"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
}

// Status returns the live connection state of every server the manager knows
// about: connected servers, servers with a reconnect in flight, and servers in
// post-failure cooldown. Callers that also want to show configured-but-never-
// connected servers merge this against their config (absent name ⇒ disconnected).
func (m *Manager) Status() []ServerStatus {
	m.cooldownMu.Lock()
	reconnecting := make(map[string]bool, len(m.reconnecting))
	for k := range m.reconnecting {
		reconnecting[k] = true
	}
	cooldown := make(map[string]time.Time, len(m.cooldownUntil))
	for k, v := range m.cooldownUntil {
		if m.now().Before(v) {
			cooldown[k] = v
		}
	}
	m.cooldownMu.Unlock()

	out := make([]ServerStatus, 0)
	seen := make(map[string]bool)

	for name, conn := range m.GetServers() {
		seen[name] = true
		st := ServerStatus{
			Name:      name,
			State:     StateConnected,
			Transport: effectiveTransport(conn.cfg),
			ToolCount: len(conn.Tools),
		}
		if reconnecting[name] {
			st.State = StateReconnecting
		}
		out = append(out, st)
	}

	// Servers not currently connected: reconnecting takes precedence over cooldown.
	for name := range reconnecting {
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, ServerStatus{Name: name, State: StateReconnecting})
	}
	for name, until := range cooldown {
		if seen[name] {
			continue
		}
		out = append(out, ServerStatus{Name: name, State: StateCooldown, CooldownUntil: until})
	}

	return out
}

// effectiveTransport resolves the transport a server config uses, mirroring the
// auto-detection in ConnectServer (URL ⇒ http, command ⇒ stdio).
func effectiveTransport(cfg config.MCPServerConfig) string {
	if cfg.Type != "" {
		return cfg.Type
	}
	if cfg.URL != "" {
		return "http"
	}
	if cfg.Command != "" {
		return "stdio"
	}
	return ""
}

// isConnectionError reports whether err indicates the transport/session died
// (rather than the server returning a normal tool error). Only these are retried:
// a fresh connection is warranted and the request most likely never reached the
// server, so a retry does not risk double-executing a side effect the server
// already performed. This is intentionally connection-level only.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, transport.ErrTransportClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"transport closed",
		"connection has been closed",
		"connection refused",
		"connection reset",
		"broken pipe",
		"use of closed network connection",
		"server closed",
		"eof",
		"no such host",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// reconnect tears down a server's dead connection and re-establishes it, honoring
// a per-server cooldown after a failed attempt so a persistently-down upstream is
// not hammered on every call. Returns nil only when a live session is restored.
func (m *Manager) reconnect(ctx context.Context, name string, cfg config.MCPServerConfig) error {
	if m.closed.Load() {
		return errors.New("manager is closed")
	}
	if until, ok := m.reconnectCooldownUntil(name); ok {
		return errors.New("server " + name + " is in reconnect cooldown until " + until.Format(time.RFC3339))
	}

	m.setReconnecting(name, true)
	defer m.setReconnecting(name, false)

	m.disconnect(name)
	if err := m.ConnectServer(ctx, name, cfg); err != nil {
		m.markReconnectFailed(name)
		logger.ErrorCF("mcp", "MCP reconnect failed; server in cooldown",
			map[string]any{
				"server":         name,
				"error":          err.Error(),
				"cooldown_until": m.now().Add(m.reconnectCooldown).Format(time.RFC3339),
			})
		return err
	}
	m.clearReconnectCooldown(name)
	logger.InfoCF("mcp", "MCP server reconnected", map[string]any{"server": name})
	return nil
}

// setReconnecting marks (or clears) an in-flight reconnect for name so Status can
// report the transient "reconnecting" state.
func (m *Manager) setReconnecting(name string, active bool) {
	m.cooldownMu.Lock()
	defer m.cooldownMu.Unlock()
	if active {
		m.reconnecting[name] = true
	} else {
		delete(m.reconnecting, name)
	}
}

// now returns the current time; a single seam so the cooldown math is easy to
// reason about (and to stub in tests if ever needed).
func (m *Manager) now() time.Time { return time.Now() }

// reconnectCooldownUntil reports the cooldown expiry for name if one is active.
func (m *Manager) reconnectCooldownUntil(name string) (time.Time, bool) {
	m.cooldownMu.Lock()
	defer m.cooldownMu.Unlock()
	until, ok := m.cooldownUntil[name]
	if !ok || !m.now().Before(until) {
		return time.Time{}, false
	}
	return until, true
}

// markReconnectFailed starts a cooldown window for name.
func (m *Manager) markReconnectFailed(name string) {
	m.cooldownMu.Lock()
	defer m.cooldownMu.Unlock()
	m.cooldownUntil[name] = m.now().Add(m.reconnectCooldown)
}

// clearReconnectCooldown drops any cooldown for name after a successful reconnect.
func (m *Manager) clearReconnectCooldown(name string) {
	m.cooldownMu.Lock()
	defer m.cooldownMu.Unlock()
	delete(m.cooldownUntil, name)
}

// startProbe launches a liveness-probe goroutine for a server and returns its stop
// channel. Caller holds m.mu. The goroutine is tracked by probeWg so Close can wait
// for it to exit.
func (m *Manager) startProbe(name string) chan struct{} {
	stop := make(chan struct{})
	interval := m.probeInterval
	m.probeWg.Add(1)
	go func() {
		defer m.probeWg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if m.closed.Load() {
					return
				}
				m.probeOnce(name)
			}
		}
	}()
	return stop
}

// probeOnce pings a server; on failure it proactively reconnects so the next real
// call finds a live session. Cooldown gating inside reconnect prevents a flapping
// server from being reconnected on every tick.
func (m *Manager) probeOnce(name string) {
	m.mu.RLock()
	conn, ok := m.servers[name]
	m.mu.RUnlock()
	if !ok {
		return
	}

	timeout := m.probeInterval
	if timeout <= 0 || timeout > probeTimeoutCap {
		timeout = probeTimeoutCap
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), timeout)
	err := conn.Client.Ping(pingCtx)
	cancel()
	if err == nil {
		return
	}

	logger.WarnCF("mcp", "MCP liveness probe failed; reconnecting",
		map[string]any{"server": name, "error": err.Error()})

	rctx, rcancel := context.WithTimeout(context.Background(), m.callTimeout)
	_ = m.reconnect(rctx, name, conn.cfg)
	rcancel()
}
