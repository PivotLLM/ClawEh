// Package constants provides shared constants across the codebase.
package constants

// internalChannels defines channels that are used for internal communication
// and should not be exposed to external users or recorded as last active channel.
var internalChannels = map[string]struct{}{
	"cli":      {},
	"system":   {},
	"subagent": {},
	"recovery": {},
	// AgentMessageChannel carries agent-to-agent asks: their replies go back
	// to the asker, never to a channel.
	AgentMessageChannel: {},
}

// AgentMessageChannel is the channel of a turn started by an ask (an
// agent_message with a wait, /ask, or the forum): the final reply is handed
// to the asker and anything else the turn publishes on it is dropped.
const AgentMessageChannel = "agent_message"

// IsInternalChannel returns true if the channel is an internal channel.
func IsInternalChannel(channel string) bool {
	_, found := internalChannels[channel]
	return found
}
