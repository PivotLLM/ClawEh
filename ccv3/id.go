// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import "strings"

// maxIDLength is the longest card id.
const maxIDLength = 64

// ValidID reports whether id satisfies the card id rule, which is the agent
// id rule (config.ValidAgentID, which this package cannot import): 1 to 64
// ASCII letters (either case), digits, '-' and '_', starting with a letter
// or digit.
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

// NormalizeID is a card's identity: ids are case-insensitive, so a valid id
// is its ASCII lower case, which names the file and is what every comparison
// uses. It does not repair an invalid id; callers check ValidID first.
func NormalizeID(id string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, id)
}

// sameID reports whether a and b are valid ids naming the same card.
func sameID(a, b string) bool {
	return ValidID(a) && ValidID(b) && NormalizeID(a) == NormalizeID(b)
}
