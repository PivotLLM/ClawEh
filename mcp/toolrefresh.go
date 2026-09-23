package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/PivotLLM/ClawEh/logger"
)

// ErrUnknownServer is returned by Reconnect when the name is not a configured,
// enabled server.
var ErrUnknownServer = errors.New("unknown MCP server")

// SetToolsChangedHandler installs the callback invoked whenever a connected
// server's tool list is replaced by a different one: after a reconnect, after a
// liveness probe whose tools/list answer differs from the stored list, and after
// a tools/list_changed notification from the server. The handler runs on its own
// goroutine, never under the manager's lock and never once the manager is closed.
func (m *Manager) SetToolsChangedHandler(fn func(server string)) {
	m.toolsChangedMu.Lock()
	m.toolsChanged = fn
	m.toolsChangedMu.Unlock()
}

// notifyToolsChanged fires the tools-changed handler for name asynchronously.
// Callers must not hold m.mu (the handler is free to call back into the manager).
func (m *Manager) notifyToolsChanged(name string) {
	if m.closed.Load() {
		return
	}
	m.toolsChangedMu.Lock()
	fn := m.toolsChanged
	m.toolsChangedMu.Unlock()
	if fn == nil {
		return
	}
	go func() {
		if m.closed.Load() {
			return
		}
		fn(name)
	}()
}

// toolKey is the identity of a tool for change detection: its JSON encoding,
// which covers the name, description and input schema. A tool that cannot be
// encoded falls back to name plus description so the comparison stays stable.
func toolKey(t mcp.Tool) string {
	b, err := json.Marshal(t)
	if err != nil {
		return t.Name + "\x00" + t.Description
	}
	return string(b)
}

// toolsEqual reports whether two tool lists describe the same tools (name,
// description and input schema), ignoring order.
func toolsEqual(a, b []mcp.Tool) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make(map[string]int, len(a))
	for _, t := range a {
		keys[toolKey(t)]++
	}
	for _, t := range b {
		k := toolKey(t)
		if keys[k] == 0 {
			return false
		}
		keys[k]--
	}
	return true
}

// replaceToolsIfChanged installs tools as the stored list for name when it
// differs from the current one, and fires the tools-changed handler. The
// connection is identified by its client so a result from a superseded
// connection (reconnected or closed meanwhile) is ignored. Tools is replaced,
// never mutated in place: a copy of the connection carrying the new list is
// installed, so a holder of an older *ServerConnection keeps a consistent
// snapshot. Returns whether the list changed.
func (m *Manager) replaceToolsIfChanged(name string, c *client.Client, tools []mcp.Tool, reason string) bool {
	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		return false
	}
	cur, ok := m.servers[name]
	if !ok || cur.Client != c || toolsEqual(cur.Tools, tools) {
		m.mu.Unlock()
		return false
	}
	updated := *cur
	updated.Tools = tools
	m.servers[name] = &updated
	before := len(cur.Tools)
	m.mu.Unlock()

	logger.InfoCF("mcp", "MCP server tool list changed",
		map[string]any{"server": name, "reason": reason, "before": before, "after": len(tools)})
	m.notifyToolsChanged(name)
	return true
}

// refreshTools re-lists a server's tools and installs the result if it differs
// from the stored list.
func (m *Manager) refreshTools(ctx context.Context, name string, c *client.Client, reason string) {
	result, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		logger.WarnCF("mcp", "Failed to re-list tools from MCP server",
			map[string]any{"server": name, "reason": reason, "error": err.Error()})
		return
	}
	m.replaceToolsIfChanged(name, c, result.Tools, reason)
}

// listChangedTimeout bounds the re-list triggered by a tools/list_changed
// notification, regardless of the configured call timeout.
const listChangedTimeout = 10 * time.Second

// onToolsListChanged handles a tools/list_changed notification from the server
// behind c: re-list with a bounded context and install the result if it changed.
// A notification that arrives during disconnect or close is dropped: the
// connection must still be the live one for name, and the manager open.
func (m *Manager) onToolsListChanged(name string, c *client.Client) {
	if m.closed.Load() {
		return
	}
	cur, ok := m.GetServer(name)
	if !ok || cur.Client != c {
		return
	}
	timeout := m.callTimeout
	if timeout <= 0 || timeout > listChangedTimeout {
		timeout = listChangedTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	m.refreshTools(ctx, name, c, "notification")
}

// subscribeToolsChanged registers for the server's tools/list_changed
// notification on a freshly-initialized client. Notifications are dispatched
// synchronously from the transport's read loop, so the handler only hands the
// work to a goroutine.
//
// On a stdio or SSE transport the notification arrives on the connection's own
// stream. Streamable HTTP under protocol 2026-07-28 has no such stream: the
// client must open a subscriptions/listen request (SEP-2575) and hold it, which
// is what the returned stop function ends. The listen request is opened for
// streamable HTTP only: on stdio it would sit in the server's message loop,
// and a server that handles requests serially (mcp-go's own stdio server does)
// would answer nothing else until it was cancelled. A server that does not
// support subscriptions/listen, or a pre-2026-07-28 connection, simply leaves
// the stream unopened; the liveness probe and reconnect still refresh the list.
func (m *Manager) subscribeToolsChanged(name string, c *client.Client, transportType string) func() {
	c.OnNotification(func(n mcp.JSONRPCNotification) {
		if n.Method != mcp.MethodNotificationToolsListChanged {
			return
		}
		go m.onToolsListChanged(name, c)
	})
	if transportType != "http" {
		return nil
	}
	stop, err := c.ListenAsync(
		context.Background(),
		mcp.SubscriptionFilter{ToolsListChanged: true},
		func(err error) {
			logger.DebugCF("mcp", "MCP notification stream ended",
				map[string]any{"server": name, "error": err.Error()})
		},
	)
	if err != nil {
		// A pre-2026-07-28 connection: notifications need no opt-in there.
		logger.DebugCF("mcp", "MCP notification stream not opened",
			map[string]any{"server": name, "error": err.Error()})
		return nil
	}
	return stop
}

// Reconnect disconnects and reconnects one configured, enabled server, then
// fires the tools-changed handler if its tool list differs from before. Unlike
// the probe- and call-driven reconnects it is an explicit operator action, so a
// server in post-failure cooldown is reconnected anyway (the cooldown is cleared
// first). Returns ErrUnknownServer when name is not in the desired set.
func (m *Manager) Reconnect(ctx context.Context, name string) error {
	m.desiredMu.Lock()
	cfg, ok := m.desired[name]
	m.desiredMu.Unlock()
	if !ok {
		return ErrUnknownServer
	}
	m.clearReconnectCooldown(name)
	return m.reconnect(ctx, name, cfg)
}
