package device

// DeviceAgentInfo is the minimal agent summary the device gateway returns from
// agents.list — enough for an operator client to populate its agent picker.
type DeviceAgentInfo struct {
	ID   string
	Name string
}

// DeviceHistoryMessage is one transcript entry returned by chat.history, oldest
// first. Only user/assistant turns with text content are surfaced.
type DeviceHistoryMessage struct {
	Role    string
	Content string
}

// AgentQuerier exposes the read-only agent/session facts the device gateway needs
// to answer operator-client RPCs (agents.list, chat.history). It is implemented by
// an adapter over the agent loop in internal/gateway; defining it here keeps the
// channels layer free of a agent import (which would cycle, since agent
// imports channels).
type AgentQuerier interface {
	// Agents returns the configured agents, the default agent id, and the session
	// key the default agent's main conversation uses (the operator client echoes
	// this mainKey back as chat.history/chat.send sessionKey).
	Agents() (agents []DeviceAgentInfo, defaultID string, mainKey string)
	// DefaultAgentID returns the id of the agent that handles turns when a client
	// does not select one (node clients join its main conversation).
	DefaultAgentID() string
	// HasAgent reports whether id names an agent the loop runs (a configured
	// agent, human agents included), so a per-device assignment to it still
	// reaches it.
	HasAgent(id string) bool
	// History returns the stored transcript for a session key, oldest first.
	History(sessionKey string) []DeviceHistoryMessage
}
