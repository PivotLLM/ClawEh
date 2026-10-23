// ClawEh
// License: MIT

package agent

import (
	"github.com/PivotLLM/ClawEh/tools"
)

// AgentLoop is the core Messenger: agent_message, /ask, /whisper and the
// forum all send through it.
var _ tools.Messenger = (*AgentLoop)(nil)

// sender is who an ask or whisper is from: an agent's or a person's name,
// and for a person how they sent it, so the recipient cannot take them for
// an agent of the same name.
type sender struct {
	id   string // the agent's id; empty for a person
	name string
	note string
}

// label is the sender as the recipient sees it: "Alice", or
// "Alice (a person, via /ask on telegram)".
func (s sender) label() string {
	if s.note == "" {
		return s.name
	}
	return s.name + " (" + s.note + ")"
}

// personNote describes a person sending command on channel.
func personNote(command, channel string) string {
	return "a person, via /" + command + " on " + channel
}
