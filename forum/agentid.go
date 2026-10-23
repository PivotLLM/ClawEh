// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import "strings"

// agentKey is an agent id's identity: agent ids are case-insensitive, so
// "Bob" and "bob" are one agent. It lower-cases ASCII letters only, the
// host's rule (config.NormalizeAgentID of a valid id), which this package
// cannot import.
func agentKey(id string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, strings.TrimSpace(id))
}

// sameAgent reports whether a and b name the same agent: neither is blank and
// their identities are equal. Every comparison of agent ids in the forum goes
// through it.
func sameAgent(a, b string) bool {
	ka := agentKey(a)
	return ka != "" && ka == agentKey(b)
}
