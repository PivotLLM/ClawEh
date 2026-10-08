// ClawEh
// License: MIT

package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// HumanProtocol is the provider protocol of a model that represents a person.
// An agent whose model is on it is a human agent: it runs no model, and its
// turns are answered by the person in the chat its default binding names.
const HumanProtocol = "human"

// IsHumanProtocol reports whether protocol is the human protocol.
func IsHumanProtocol(protocol string) bool { return protocol == HumanProtocol }

// IsHumanModel reports whether name is the model_name of a model on a human
// provider. Only the model_name counts: a person is named, never addressed
// by a wire model id. Disabled entries count: a disabled human model is still
// a person.
func (c *Config) IsHumanModel(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for i := range c.Models {
		if c.Models[i].ModelName == name && c.modelIsHuman(&c.Models[i]) {
			return true
		}
	}
	return false
}

func (c *Config) modelIsHuman(m *ModelConfig) bool {
	p, err := c.GetProvider(m.Provider)
	return err == nil && IsHumanProtocol(p.Protocol)
}

// HumanModelOf returns the first model in the agent's own model list that
// represents a person. An agent with no models of its own uses the defaults,
// which may never name a human model.
func (c *Config) HumanModelOf(ac *AgentConfig) (string, bool) {
	if ac == nil {
		return "", false
	}
	for _, m := range ac.Models {
		if c.IsHumanModel(m) {
			return m, true
		}
	}
	return "", false
}

// IsHumanAgent reports whether the configured agent agentID represents a
// person: its model list names a human model.
func (c *Config) IsHumanAgent(agentID string) bool {
	_, ok := c.HumanModelOf(c.AgentByID(agentID))
	return ok
}

// HumanRequestTimeout is how long a turn to the human agent agentID waits for
// the person's answer, in seconds: its model's request_timeout, else
// agents.defaults.request_timeout. 0 means no limit of its own.
func (c *Config) HumanRequestTimeout(agentID string) int {
	model, ok := c.HumanModelOf(c.AgentByID(agentID))
	if !ok {
		return 0
	}
	for i := range c.Models {
		m := &c.Models[i]
		if m.ModelName == model && m.RequestTimeout > 0 && c.modelIsHuman(m) {
			return m.RequestTimeout
		}
	}
	return c.Agents.Defaults.RequestTimeout
}

// sameAgentID matches a binding's agent id to an agent id the way
// DefaultBinding does: case-insensitively, the agent id trimmed.
func sameAgentID(bindingAgentID, agentID string) bool {
	return strings.EqualFold(bindingAgentID, strings.TrimSpace(agentID))
}

// HumanProblemKind says which rule a human-agent problem breaks.
type HumanProblemKind int

const (
	// HumanExtraModels: a human agent lists other models besides the person.
	HumanExtraModels HumanProblemKind = iota + 1
	// HumanNoChat: a human agent has no default binding naming a chat.
	HumanNoChat
	// HumanBindings: a human agent has more than one binding, or its default
	// routes a whole channel to it rather than naming one chat.
	HumanBindings
	// HumanSharedChat: a human agent's chat is also bound to another agent.
	HumanSharedChat
	// HumanDefault: a human agent is marked the default agent.
	HumanDefault
	// HumanModelMisused: a human model is used where a model must answer
	// (defaults, summarization, vision, image, sub-agent models).
	HumanModelMisused
	// HumanSharedName: a human model's model_name is also another model's.
	HumanSharedName
)

// HumanProblem is one way the configuration breaks the human-agent rules.
// Agent is the agent concerned (empty for a global setting); Site is the
// model reference site for HumanModelMisused; Model is the human model.
// Message is one plain sentence for the operator.
type HumanProblem struct {
	Kind    HumanProblemKind
	Agent   string
	Site    string
	Model   string
	Message string
}

// key identifies the problem across edits: kind, agent and site, and the
// model for a shared model name (which has neither agent nor site).
func (p HumanProblem) key() string {
	k := fmt.Sprintf("%d|%s|%s", p.Kind, strings.ToLower(strings.TrimSpace(p.Agent)), p.Site)
	if p.Kind == HumanSharedName {
		k += "|" + p.Model
	}
	return k
}

// SetsAgentAside reports whether the problem keeps its agent from running
// (PruneHumanProblems disables it). The others are a value ignored where it
// is set.
func (p HumanProblem) SetsAgentAside() bool {
	return p.Kind != HumanModelMisused && p.Kind != HumanSharedName
}

// HumanProblems lists every way the configuration breaks the human-agent
// rules, for enabled agents (a disabled one is checked only for its chat,
// which it keeps):
//   - a human agent has only that one model, so no other model could ever
//     answer in the person's place;
//   - it has exactly one binding: its default, naming one chat;
//   - that chat is bound to no other agent;
//   - it is never the default agent;
//   - a human model is never anyone's default, summarization, vision, image
//     or sub-agent model, and its model_name is no other model's.
func (c *Config) HumanProblems() []HumanProblem {
	var out []HumanProblem
	for i := range c.Agents.List {
		ac := &c.Agents.List[i]
		model, ok := c.HumanModelOf(ac)
		if !ok {
			continue
		}
		name := ac.DisplayName()
		add := func(kind HumanProblemKind, msg string) {
			out = append(out, HumanProblem{Kind: kind, Agent: ac.ID, Model: model, Message: msg})
		}
		if !ac.IsEnabled() {
			// A disabled human agent still owns its chat (messages there are
			// answered "not running", never routed to another agent), so no
			// other agent may be bound to it.
			if p, ok := c.humanChatProblem(ac, name); ok && p.Kind == HumanSharedChat {
				add(p.Kind, p.Message)
			}
			continue
		}
		if len(ac.Models) > 1 {
			add(HumanExtraModels, fmt.Sprintf("%s represents a person, so %s must be its only model.", name, model))
		}
		if ac.Default {
			add(HumanDefault, name+" represents a person and can't be the default agent.")
		}
		if p, ok := c.humanChatProblem(ac, name); ok {
			add(p.Kind, p.Message)
		}
	}
	for _, site := range c.modelRefSites() {
		if site.own {
			continue // an agent's own model list: the rules above
		}
		for _, v := range site.values() {
			if c.IsHumanModel(v) {
				out = append(out, HumanProblem{
					Kind: HumanModelMisused, Agent: site.agent, Site: site.where, Model: v,
					Message: fmt.Sprintf("%s represents a person and can't be %s.", v, site.role),
				})
			}
		}
	}
	reported := map[string]bool{}
	for i := range c.Models {
		name := c.Models[i].ModelName
		if reported[name] || !c.IsHumanModel(name) {
			continue
		}
		for j := range c.Models {
			if c.Models[j].ModelName == name && !c.modelIsHuman(&c.Models[j]) {
				reported[name] = true
				out = append(out, HumanProblem{
					Kind: HumanSharedName, Model: name,
					Message: fmt.Sprintf("The model name %s is used by a person and by another model.", name),
				})
				break
			}
		}
	}
	return out
}

// humanChatProblem checks a human agent's bindings: exactly one, its
// default, naming one chat that no other agent is bound to.
func (c *Config) humanChatProblem(ac *AgentConfig, name string) (HumanProblem, bool) {
	own := 0
	for i := range c.Bindings {
		if sameAgentID(c.Bindings[i].AgentID, ac.ID) {
			own++
		}
	}
	def, hasDefault := c.DefaultBinding(ac.ID)
	channel, chatID, _, found := c.CronTarget(ac.ID)
	switch {
	case own > 1:
		return HumanProblem{Kind: HumanBindings, Message: name + " must have exactly one chat: its default."}, true
	case !found || !hasDefault:
		return HumanProblem{Kind: HumanNoChat, Message: name + " needs a default chat where the person is reached."}, true
	case def.Match.Peer == nil || def.Match.Peer.ID == "":
		return HumanProblem{Kind: HumanBindings, Message: name + " needs a chat of its own, not a whole channel or bot."}, true
	}
	if other, shared := c.chatBoundElsewhere(ac.ID, channel, chatID); shared {
		return HumanProblem{Kind: HumanSharedChat, Message: fmt.Sprintf("%s's chat is also used by %s.", name, other)}, true
	}
	return HumanProblem{}, false
}

// chatBoundElsewhere reports whether a binding of an agent other than
// agentID names the chat channel:chatID, as a routing peer or a delivery
// target, and returns that agent's name.
func (c *Config) chatBoundElsewhere(agentID, channel, chatID string) (string, bool) {
	for i := range c.Bindings {
		b := &c.Bindings[i]
		if sameAgentID(b.AgentID, agentID) || b.Match.Channel != channel {
			continue
		}
		if (b.Match.Peer != nil && b.Match.Peer.ID == chatID) || b.DeliverTo == chatID {
			if ac := c.AgentByID(b.AgentID); ac != nil {
				return ac.DisplayName(), true
			}
			return b.AgentID, true
		}
	}
	return "", false
}

// newHumanProblems returns the human-agent problems next has that before
// does not, as errors. A missing default chat is not refused: an agent must
// exist before a binding can name it, so a human agent is set up in two
// saves; until it has a chat it is not run (PruneHumanProblems) and the
// Agents page says why.
//
// A problem is the same one when its kind, agent and site match (the model
// too, for a shared model name), not its text, so renaming an agent does not
// turn an old problem into a new one.
func newHumanProblems(before, next *Config) []error {
	return newProblems(before.HumanProblems(), next.HumanProblems(), HumanProblem.key,
		func(p HumanProblem) error {
			if p.Kind == HumanNoChat {
				return nil
			}
			return errors.New(p.Message)
		})
}

// PruneHumanProblems makes the running copy obey the human-agent rules and
// returns what it changed: an agent breaking them is disabled, a human model
// used where a model must answer is removed from that site, and a model that
// shares a human model's name is dropped. It mutates the in-memory config
// only; the file keeps the entries so the operator can repair them, and the
// WebUI shows each problem where it is configured.
func (c *Config) PruneHumanProblems() []HumanProblem {
	problems := c.HumanProblems()
	off := false
	for _, p := range problems {
		if p.SetsAgentAside() {
			if ac := c.AgentByID(p.Agent); ac != nil {
				ac.Enabled = &off
			}
		}
	}
	for _, site := range c.modelRefSites() {
		switch {
		case site.own:
		case site.slice == nil:
			if c.IsHumanModel(*site.scalar) {
				*site.scalar = ""
			}
		default:
			*site.slice = slices.DeleteFunc(*site.slice, c.IsHumanModel)
		}
	}
	humanNames := map[string]bool{}
	for i := range c.Models {
		if c.modelIsHuman(&c.Models[i]) {
			humanNames[c.Models[i].ModelName] = true
		}
	}
	c.Models = slices.DeleteFunc(c.Models, func(m ModelConfig) bool {
		return humanNames[m.ModelName] && !c.modelIsHuman(&m)
	})
	return problems
}
