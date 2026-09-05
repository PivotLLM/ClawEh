// ClawEh
// License: MIT

package agenttoken

import "regexp"

// redactPattern matches any token-shaped substring in text: AGT or SST
// followed by exactly 64 lowercase hex characters.
var redactPattern = regexp.MustCompile(`(?:AGT|SST)[0-9a-f]{64}`)

// Redaction is the placeholder substituted for redacted tokens.
const Redaction = "AGT[REDACTED]"

// Redact replaces every token-shaped substring (AGT or SST followed by 64
// hex chars) with the Redaction placeholder, except for the sub-agent
// sentinel which is left intact (it is not secret).
func Redact(s string) string {
	if s == "" {
		return s
	}
	return redactPattern.ReplaceAllStringFunc(s, func(match string) string {
		if match == SubagentSentinel {
			return match
		}
		return Redaction
	})
}
