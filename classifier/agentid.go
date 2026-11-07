// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import "strings"

// maxIDLength is the longest classifier or agent id.
const maxIDLength = 64

// ValidID reports whether id satisfies the agent id rule (config.ValidAgentID,
// which this package cannot import): 1 to 64 ASCII letters (either case),
// digits, '-' and '_', starting with a letter or digit. Classifier ids follow
// the same rule; both are checked against config/testdata/agent_id_cases.json.
func ValidID(id string) bool {
	if id == "" || len(id) > maxIDLength {
		return false
	}
	for i := range len(id) {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// NormalizeID is an id's identity: ids are case-insensitive, so a valid id is
// its ASCII lower case, which names the definition file and is what every
// comparison uses. It does not repair an invalid id; callers check ValidID
// first.
func NormalizeID(id string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, strings.TrimSpace(id))
}

// sameAgent reports whether a and b name the same agent: neither is blank and
// their identities are equal. Every comparison of agent ids in the package
// goes through it.
func sameAgent(a, b string) bool {
	ka := NormalizeID(a)
	return ka != "" && ka == NormalizeID(b)
}
