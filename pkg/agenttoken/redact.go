// ClawEh
// License: MIT

package agenttoken

import "regexp"

// redactPattern matches any agent-token-shaped substring anywhere in text.
var redactPattern = regexp.MustCompile(`AGT[0-9a-f]{64}`)

// Redaction is the placeholder substituted for redacted tokens.
const Redaction = "[REDACTED-AGT]"

// Redact replaces every `AGT[0-9a-f]{64}` substring with [REDACTED-AGT],
// except for the sub-agent sentinel which is left intact (it is not secret).
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
