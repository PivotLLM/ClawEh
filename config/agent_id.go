// ClawEh
// License: MIT

package config

import (
	"regexp"
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
// agentID: the list names it (after NormalizeAgentID) or holds "*". A nil
// receiver or list allows nothing.
func (s *SubagentsConfig) Allows(agentID string) bool {
	if s == nil {
		return false
	}
	target := NormalizeAgentID(agentID)
	for _, allowed := range s.AllowAgents {
		if allowed == "*" || NormalizeAgentID(allowed) == target {
			return true
		}
	}
	return false
}
