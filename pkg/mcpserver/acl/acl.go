// ClawEh
// License: MIT

// Package acl is the per-agent authorization extension point evaluated on
// every MCP tools/call after the agent_token has been resolved to an agent
// identity. tools/list is not gated here — tool enumeration is global.
package acl

// Policy decides whether the named agent may invoke the named tool. It is
// consulted exactly once per tools/call, immediately after the token has
// been validated and resolved to agent. Implementations must be safe for
// concurrent use; the dispatcher calls Policy from many goroutines.
type Policy interface {
	IsAllowed(agent, tool string) bool
}

// PolicyFunc is a function adapter so callers can supply ad-hoc policies
// (typically tests) without declaring a type.
type PolicyFunc func(agent, tool string) bool

// IsAllowed satisfies Policy.
func (f PolicyFunc) IsAllowed(agent, tool string) bool { return f(agent, tool) }

// Default is the open-by-default policy: every (agent, tool) pair is
// allowed. Per-agent restrictions plug in by replacing the Policy passed
// to the MCP server — extend this package or supply an alternative
// implementation. Do not embed restriction logic in the dispatcher.
var Default Policy = PolicyFunc(func(_, _ string) bool { return true })
