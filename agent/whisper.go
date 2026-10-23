// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
)

// agentMessageToolName is the tool a whisper's recipient answers with.
const agentMessageToolName = "agent_message"

// whisper is one held message.
type whisper struct {
	fromID string // the sending agent's id; empty for a person
	from   string
	note   string // how a person sent it, e.g. "a person, via /whisper on telegram"; empty for an agent
	text   string
}

// whisperStore holds whispers per agent until its next message. It is in
// memory: whispers not yet delivered are lost on restart. The zero value is
// ready to use.
type whisperStore struct {
	mu   sync.Mutex
	held map[string][]whisper
}

func (s *whisperStore) add(agentID string, w whisper) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held == nil {
		s.held = make(map[string][]whisper)
	}
	s.held[agentID] = append(s.held[agentID], w)
}

// take removes and returns agentID's held whispers, oldest first.
func (s *whisperStore) take(agentID string) []whisper {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.held[agentID]
	delete(s.held, agentID)
	return w
}

// drop forgets agentID's whispers (a deleted temporary agent).
func (s *whisperStore) drop(agentID string) {
	s.take(agentID)
}

// prune forgets the whispers of every agent exists reports gone (an agent
// removed from the configuration).
func (s *whisperStore) prune(exists func(agentID string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.held {
		if !exists(id) {
			delete(s.held, id)
		}
	}
}

// whisperBlock renders held whispers for the start of the next message. The
// hint on answering is given only for a whisper canAnswer (nil: none) says
// the recipient can answer.
func whisperBlock(ws []whisper, canAnswer func(w whisper) bool) string {
	lines := make([]string, 0, len(ws))
	for _, w := range ws {
		head := "[Private whisper from " + sender{name: w.from, note: w.note}.label() + " — no reply expected."
		if canAnswer != nil && canAnswer(w) {
			head += " To answer privately, use " + agentMessageToolName + " with wait_seconds 0."
		}
		lines = append(lines, head+"] "+w.text)
	}
	return strings.Join(lines, "\n")
}

// prependWhispers adds agent's held whispers to the start of message,
// removing them so each is delivered exactly once.
func (al *AgentLoop) prependWhispers(agent *AgentInstance, message string) string {
	ws := al.whispers.take(agent.ID)
	if len(ws) == 0 {
		return message
	}
	logger.InfoCF("agent", "Delivering whispers",
		map[string]any{"agent_id": agent.ID, "count": len(ws)})
	return whisperBlock(ws, al.canAnswerWhisper(agent)) + "\n\n" + message
}

// canAnswerWhisper returns whether agent can answer a whisper privately: it
// has agent_message and the whisper's sender is an agent in its
// subagents.allow_agents. A person's whisper cannot be answered that way.
func (al *AgentLoop) canAnswerWhisper(agent *AgentInstance) func(w whisper) bool {
	if _, ok := agent.Tools.Get(agentMessageToolName); !ok {
		return nil
	}
	return func(w whisper) bool {
		return w.fromID != "" && newAgentServices(al, agent.ID).CanTarget(w.fromID)
	}
}

// Whisper implements tools.Messenger: message is held for agentID and added,
// marked private, to the start of the next message it receives, whatever its
// source. It never starts a turn.
func (al *AgentLoop) Whisper(ctx context.Context, fromID, from, agentID, message string) error {
	return al.whisper(ctx, sender{id: strings.TrimSpace(fromID), name: strings.TrimSpace(from)}, agentID, message)
}

// whisper is Whisper from from.
func (al *AgentLoop) whisper(_ context.Context, from sender, agentID, message string) error {
	if from.name == "" {
		return errors.New("whisper: the sender is required")
	}
	if strings.TrimSpace(message) == "" {
		return errors.New("whisper: the message is empty")
	}
	target, ok := al.GetRegistry().Get(agentID)
	if !ok || target == nil {
		return fmt.Errorf("%w: %s", tools.ErrNoSuchAgent, agentID)
	}
	al.whispers.add(target.ID, whisper{fromID: from.id, from: from.name, note: from.note, text: message})
	logger.InfoCF("agent", "Whisper held for the agent's next message",
		map[string]any{"agent_id": target.ID, "from": from.label()})
	return nil
}
