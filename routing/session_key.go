package routing

import (
	"fmt"
	"strings"
)

// RoutePeer represents a chat peer with kind and ID.
type RoutePeer struct {
	Kind string // "direct", "group", "channel"
	ID   string
}

// ParsedSessionKey is the result of parsing an agent-scoped session key.
type ParsedSessionKey struct {
	AgentID string
	Rest    string
}

// BuildAgentMainSessionKey returns "agent:<agentId>:main".
func BuildAgentMainSessionKey(agentID string) string {
	return fmt.Sprintf("agent:%s:%s", NormalizeAgentID(agentID), DefaultMainKey)
}

// ResolveAgentSessionKey returns the session a turn for agentID runs in, given
// a session key the caller asked for. Every agent has exactly one persistent
// conversation, agent:<id>:main: its cognitive memory is fed from one session
// only, so no surface (a channel, a device, an MCP token, a bus message with an
// explicit key) may open a second one. The one exception is the agent's own
// ephemeral sub-agent session (agent:<id>:subagent:<uuid>), which is returned
// unchanged; any other key, including one naming another agent, resolves to
// agentID's main session.
func ResolveAgentSessionKey(agentID, requested string) string {
	requested = strings.TrimSpace(requested)
	if pk := ParseAgentSessionKey(requested); pk != nil &&
		NormalizeAgentID(pk.AgentID) == NormalizeAgentID(agentID) &&
		IsSubagentSessionKey(requested) {
		return requested
	}
	return BuildAgentMainSessionKey(agentID)
}

// AgentIDFromSessionKey extracts the agent id from an agent-scoped session key
// ("agent:<id>:..."). It returns "" for the bare "main" sentinel a node client
// sends and for any key that is not agent-scoped, so the caller falls back to
// its own default.
func AgentIDFromSessionKey(sessionKey string) string {
	parts := strings.Split(strings.TrimSpace(sessionKey), ":")
	if len(parts) < 2 || parts[0] != "agent" {
		return ""
	}
	id := strings.TrimSpace(parts[1])
	if id == "" || id == DefaultMainKey {
		return ""
	}
	return id
}

// ParseAgentSessionKey extracts agentId and rest from "agent:<agentId>:<rest>".
func ParseAgentSessionKey(sessionKey string) *ParsedSessionKey {
	raw := strings.TrimSpace(sessionKey)
	if raw == "" {
		return nil
	}
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) < 3 {
		return nil
	}
	if parts[0] != "agent" {
		return nil
	}
	agentID := strings.TrimSpace(parts[1])
	rest := parts[2]
	if agentID == "" || rest == "" {
		return nil
	}
	return &ParsedSessionKey{AgentID: agentID, Rest: rest}
}

// IsSubagentSessionKey returns true if the session key represents a subagent.
func IsSubagentSessionKey(sessionKey string) bool {
	raw := strings.TrimSpace(sessionKey)
	if raw == "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(raw), "subagent:") {
		return true
	}
	parsed := ParseAgentSessionKey(raw)
	if parsed == nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(parsed.Rest), "subagent:")
}
