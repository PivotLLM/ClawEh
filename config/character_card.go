// ClawEh
// License: MIT

package config

import (
	"errors"
	"strconv"
)

// CardsOnHumanAgents lists the human agents (an agent whose model is a human
// model, HumanModelOf) that name a character card. A human agent renders no
// prompt, so the card would be ignored; Store.Update refuses a save that
// introduces one, and the Agents page and Check Up show the ones already in
// the file. Ids are the configured ids.
func (c *Config) CardsOnHumanAgents() []string {
	var out []string
	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		if a.CharacterCard == "" {
			continue
		}
		if _, human := c.HumanModelOf(a); human {
			out = append(out, a.ID)
		}
	}
	return out
}

// newCardsOnHumanAgents is Store.Update's refusal: one error per human agent
// that next gives a character card and before did not.
func newCardsOnHumanAgents(before, next *Config) []error {
	return newProblems(before.CardsOnHumanAgents(), next.CardsOnHumanAgents(), NormalizeAgentID,
		func(id string) error {
			name := id
			if ac := next.AgentByID(id); ac != nil {
				name = ac.DisplayName()
			}
			return errors.New(name + " represents a person and cannot have a character card " + strconv.Quote(next.AgentByID(id).CharacterCard) + ".")
		})
}
