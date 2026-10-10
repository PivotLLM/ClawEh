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

// maxAgentIDLength is the longest normalized agent id.
const maxAgentIDLength = 64

var (
	agentIDValidRe        = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	agentIDInvalidCharsRe = regexp.MustCompile(`[^a-z0-9_-]+`)
	agentIDLeadingDashRe  = regexp.MustCompile(`^-+`)
	agentIDTrailingDashRe = regexp.MustCompile(`-+$`)
)

// NormalizeAgentID sanitizes an agent ID to [a-z0-9][a-z0-9_-]{0,63}.
// Invalid characters are collapsed to "-". Leading/trailing dashes stripped.
// Empty input returns "main". This is the rule the runtime routes by
// (routing.NormalizeAgentID delegates here).
func NormalizeAgentID(id string) string {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return defaultAgentID
	}
	lower := strings.ToLower(trimmed)
	if agentIDValidRe.MatchString(lower) {
		return lower
	}
	result := agentIDInvalidCharsRe.ReplaceAllString(lower, "-")
	result = agentIDLeadingDashRe.ReplaceAllString(result, "")
	result = agentIDTrailingDashRe.ReplaceAllString(result, "")
	if len(result) > maxAgentIDLength {
		result = result[:maxAgentIDLength]
	}
	if result == "" {
		return defaultAgentID
	}
	return result
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

// agentIDError says why id, an agent id or a reference to one, is not in the
// form NormalizeAgentID gives, or returns nil when it is. where names the
// place a reference was found (" in bindings"); it is empty for
// agents.list[].id. ClawEh never rewrites an id: the operator fixes it.
func agentIDError(id, where string) error {
	// The messages are sentences an operator reads (WebUI, startup), built by
	// concatenation like the other config refusals (mount_names.go).
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		if where == "" {
			return errors.New("An agent has no id; give it one, such as " + strconv.Quote("alice") + ".")
		}
		return errors.New("An agent id" + where + " is empty; name the agent, such as " + strconv.Quote("alice") + ".")
	}
	norm := NormalizeAgentID(id)
	if norm == id {
		return nil
	}
	lower := strings.ToLower(trimmed)
	var problem string
	switch {
	case len(lower) > maxAgentIDLength && !agentIDInvalidCharsRe.MatchString(lower):
		problem = "is longer than " + strconv.Itoa(maxAgentIDLength) + " characters"
	case strings.HasPrefix(lower, "-") && !agentIDInvalidCharsRe.MatchString(lower):
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
// is not in normal form (NormalizeAgentID): agents.list[].id,
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
