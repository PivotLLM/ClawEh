package gateway

import (
	"strings"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/channels/device"
	"github.com/PivotLLM/ClawEh/routing"
)

// deviceAgentQuerier adapts the agent loop to the device channel's read-only
// AgentQuerier. It lives here (not in channels/device) because agent
// imports channels, so the device package cannot import agent.
type deviceAgentQuerier struct{ al *agent.AgentLoop }

// Agents lists configured agents plus the default agent's id and main session key.
func (q deviceAgentQuerier) Agents() ([]device.DeviceAgentInfo, string, string) {
	reg := q.al.GetRegistry()
	defaultID := reg.GetDefaultAgentID()
	ids := reg.ListAgentIDs()
	out := make([]device.DeviceAgentInfo, 0, len(ids))
	for _, id := range ids {
		info := device.DeviceAgentInfo{ID: id, Name: id}
		if inst, ok := reg.GetAgent(id); ok && inst.Name != "" {
			info.Name = inst.Name
		}
		// Always carry a non-empty name: operator clients hide entries without a
		// display label, so fall back to the id for agents with no configured name.
		out = append(out, info)
	}
	return out, defaultID, routing.BuildAgentMainSessionKey(defaultID)
}

// DefaultAgentID returns the registry's default agent id.
func (q deviceAgentQuerier) DefaultAgentID() string {
	return q.al.GetRegistry().GetDefaultAgentID()
}

// SessionMode returns the configured session mode, so the device gateway can
// join the agent's main conversation when sessions are unified. An unreadable
// config yields "", which IsUnified treats as unified — the safe default, since
// unified is what every other surface falls back to.
func (q deviceAgentQuerier) SessionMode() string {
	cfg := q.al.GetConfig()
	if cfg == nil {
		return ""
	}
	return cfg.Session.Mode
}

// History returns the user/assistant text turns stored for a session key.
func (q deviceAgentQuerier) History(sessionKey string) []device.DeviceHistoryMessage {
	reg := q.al.GetRegistry()
	inst := agentForSessionKey(reg, sessionKey)
	if inst == nil || inst.Sessions == nil {
		return nil
	}
	msgs := inst.Sessions.GetHistory(sessionKey)
	out := make([]device.DeviceHistoryMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		out = append(out, device.DeviceHistoryMessage{Role: m.Role, Content: m.Content})
	}
	return out
}

// agentForSessionKey resolves the agent that owns a session key of the form
// "agent:<id>:...", falling back to the default agent.
func agentForSessionKey(reg *agent.AgentRegistry, sessionKey string) *agent.AgentInstance {
	if strings.HasPrefix(sessionKey, "agent:") {
		parts := strings.SplitN(sessionKey, ":", 3)
		if len(parts) >= 2 {
			if inst, ok := reg.GetAgent(parts[1]); ok {
				return inst
			}
		}
	}
	return reg.GetDefaultAgent()
}

// injectDeviceAgentQuerier wires the agent loop into the device channel (if
// enabled) so it can serve agents.list / chat.history to operator clients.
func injectDeviceAgentQuerier(cm *channels.Manager, al *agent.AgentLoop) {
	ch, ok := cm.Channel("device")
	if !ok {
		return
	}
	if setter, ok := ch.(interface{ SetAgentQuerier(device.AgentQuerier) }); ok {
		setter.SetAgentQuerier(deviceAgentQuerier{al: al})
	}
}
