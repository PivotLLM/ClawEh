// Shared definitions for the provider management UI.

// PROTOCOL_OPTIONS is what the wire-protocol picker offers. "gemini-cli" is
// deliberately absent: Google deprecated the Gemini CLI and it now runs
// Antigravity. Existing configs naming it keep working — the backend treats it
// as an alias — but nothing new should be created with it.
export const PROTOCOL_OPTIONS = [
  "openai-chat",
  "openai-responses",
  "azure",
  "anthropic",
  "anthropic-messages",
  "claude-cli",
  "codex-cli",
  "antigravity-cli",
  "cursor-cli",
] as const

export type Protocol = (typeof PROTOCOL_OPTIONS)[number]

// CLI_PROTOCOLS still includes the "gemini-cli" alias, unlike the picker: a
// provider already carrying it must keep rendering as a CLI card, with a
// command field rather than a base URL and API key.
const CLI_PROTOCOLS: ReadonlySet<string> = new Set([
  "claude-cli",
  "codex-cli",
  "antigravity-cli",
  "gemini-cli",
  "cursor-cli",
])

// isCliProtocol reports whether a protocol is subprocess-based — these use a
// `command` and have no base_url / api_key.
export function isCliProtocol(protocol: string): boolean {
  return CLI_PROTOCOLS.has(protocol)
}

// requiresBaseURL reports whether base_url is required for a protocol.
export function requiresBaseURL(protocol: string): boolean {
  return (
    protocol === "openai-chat" ||
    protocol === "openai-responses" ||
    protocol === "azure" ||
    protocol === "anthropic" ||
    protocol === "anthropic-messages"
  )
}
