// ClawEh
// License: MIT

package config

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// defaultAgentID is the id an empty or unusable agent id normalizes to
// (routing.DefaultAgentID).
const defaultAgentID = "main"

// maxAgentIDLength is the longest agent id.
const maxAgentIDLength = 64

// asciiSpace is the whitespace trimmed before deciding an id is empty. It is
// ASCII only so the WebUI (agent-model.ts) trims exactly the same characters.
const asciiSpace = " \t\n\v\f\r"

var agentIDInvalidCharsRe = regexp.MustCompile(`[^a-z0-9_-]+`)

// The agent id rule. An agent id is 1 to 64 characters of lower-case a-z,
// 0-9, '-' and '_', and starts with a letter or digit. ValidAgentID checks it;
// NormalizeAgentID turns any input into an id that satisfies it and returns a
// valid id unchanged. The WebUI implements the identical rule
// (web/frontend/src/components/agents/agent-model.ts), and both are checked
// against testdata/agent_id_cases.json.

// ValidAgentID reports whether id satisfies the agent id rule.
func ValidAgentID(id string) bool {
	if id == "" || len(id) > maxAgentIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// NormalizeAgentID turns any input into a valid agent id: ASCII letters are
// lower-cased, every run of other characters outside a-z, 0-9, '-' and '_'
// becomes one '-', leading and trailing '-' and '_' are removed, and the
// result is cut to 64 characters without a trailing '-' or '_'. Input that
// leaves nothing normalizes to "main". A valid id is returned unchanged. This
// is the rule the runtime routes by (routing.NormalizeAgentID delegates here).
func NormalizeAgentID(id string) string {
	lower := asciiLower(id)
	if ValidAgentID(lower) {
		return lower
	}
	result := strings.Trim(agentIDInvalidCharsRe.ReplaceAllString(lower, "-"), "-_")
	if len(result) > maxAgentIDLength {
		result = strings.TrimRight(result[:maxAgentIDLength], "-_")
	}
	if result == "" {
		return defaultAgentID
	}
	return result
}

// asciiLower lower-cases A-Z only. Unicode case mapping differs between Go
// and JavaScript (U+0130 is one rune in Go, two in JavaScript), so the rule
// leaves every other character to become '-'.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// Allows reports whether subagents.allow_agents lets its agent start or ask
// agentID: the list names it (entries are in normal form, validateAgentIDs)
// or holds "*". A nil receiver or list allows nothing.
func (s *SubagentsConfig) Allows(agentID string) bool {
	if s == nil {
		return false
	}
	target := NormalizeAgentID(agentID)
	for _, allowed := range s.AllowAgents {
		if allowed == "*" || allowed == target {
			return true
		}
	}
	return false
}

// agentIDError says why id, an agent id or a reference to one, does not
// satisfy the agent id rule (ValidAgentID), or returns nil when it does. where names the
// place a reference was found (" in bindings"); it is empty for
// agents.list[].id. ClawEh never rewrites an id: the operator fixes it.
func agentIDError(id, where string) error {
	// The messages are sentences an operator reads (WebUI, startup), built by
	// concatenation like the other config refusals (mount_names.go).
	if ValidAgentID(id) {
		return nil
	}
	trimmed := strings.Trim(id, asciiSpace)
	if trimmed == "" {
		if where == "" {
			return errors.New("An agent has no id; give it one, such as " + strconv.Quote("alice") + ".")
		}
		return errors.New("An agent id" + where + " is empty; name the agent, such as " + strconv.Quote("alice") + ".")
	}
	norm := NormalizeAgentID(id)
	lower := asciiLower(trimmed)
	var problem string
	switch {
	case len(lower) > maxAgentIDLength && !agentIDInvalidCharsRe.MatchString(lower):
		problem = "is longer than " + strconv.Itoa(maxAgentIDLength) + " characters"
	case (lower[0] == '-' || lower[0] == '_') && !agentIDInvalidCharsRe.MatchString(lower):
		problem = "must start with a letter or digit"
	default:
		problem = "may use only lower-case letters, digits, - and _"
	}
	msg := "Agent id " + strconv.Quote(id) + where + " " + problem
	if norm == defaultAgentID && lower != defaultAgentID {
		// Nothing of the id survives normalization; "main" would be a guess.
		return errors.New(msg + ".")
	}
	return errors.New(msg + "; use " + strconv.Quote(norm) + ".")
}

// AgentIDErrors returns one error for each agent id, or reference to one, that
// does not satisfy the agent id rule (ValidAgentID): agents.list[].id,
// bindings[].agent_id and agent_mentions, and subagents.allow_agents ("*"
// allowed). A binding with no agent_id routes to the default agent and is
// allowed. LoadConfig and Store.Update refuse a config with any.
func (c *Config) AgentIDErrors() []error {
	var errs []error
	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	for i := range c.Agents.List {
		a := &c.Agents.List[i]
		add(agentIDError(a.ID, ""))
		if a.Subagents == nil {
			continue
		}
		for _, ref := range a.Subagents.AllowAgents {
			if ref != "*" {
				add(agentIDError(ref, " in "+a.DisplayName()+"'s subagents.allow_agents"))
			}
		}
	}
	for i := range c.Bindings {
		b := &c.Bindings[i]
		if b.AgentID != "" {
			add(agentIDError(b.AgentID, " in bindings"))
		}
		for _, ref := range b.AgentMentions {
			if ref != "*" {
				add(agentIDError(ref, " in bindings agent_mentions"))
			}
		}
	}
	return errs
}
