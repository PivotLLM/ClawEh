// ClawEh
// License: MIT
//
// Package agenttoken defines the token formats used by the claw MCP layer and
// the helpers that operate on them: the reserved sub-agent sentinel (used to
// deny sub-agents MCP access) and token redaction (see redact.go).
//
// The per-agent identity-token Manager was removed once the MCP server adopted
// the SST session token as its sole auth + routing credential; this package no
// longer issues tokens. The `AGT`/`SST` prefixes survive so redaction can find
// and scrub either token shape in arbitrary text.
package agenttoken

// Prefix is the magic literal at the start of a legacy agent token and the
// sub-agent sentinel. Kept so redaction can recognise the shape.
const Prefix = "AGT"

// HexLen is the number of hex characters that follow the prefix.
const HexLen = 64

// TokenLen is the total length of a prefixed token (Prefix + HexLen).
const TokenLen = len(Prefix) + HexLen

// SubagentSentinel is the reserved token value handed to sub-agents that must
// NOT be granted claw MCP access. It is not secret; the MCP server recognises
// it and refuses the call with a clear message.
const SubagentSentinel = Prefix + "0000000000000000000000000000000000000000000000000000000000000000"

// IsSubagentSentinel reports whether the token is the reserved sub-agent
// sentinel value.
func IsSubagentSentinel(token string) bool {
	return token == SubagentSentinel
}
