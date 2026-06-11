// Shared definitions for the provider management UI.

export const PROTOCOL_OPTIONS = [
  "openai",
  "azure",
  "anthropic",
  "anthropic-messages",
  "claude-cli",
  "codex-cli",
  "gemini-cli",
] as const

export type Protocol = (typeof PROTOCOL_OPTIONS)[number]

const CLI_PROTOCOLS: ReadonlySet<string> = new Set([
  "claude-cli",
  "codex-cli",
  "gemini-cli",
])

// isCliProtocol reports whether a protocol is subprocess-based — these use a
// `command` and have no base_url / api_key.
export function isCliProtocol(protocol: string): boolean {
  return CLI_PROTOCOLS.has(protocol)
}

// requiresBaseURL reports whether base_url is required for a protocol.
export function requiresBaseURL(protocol: string): boolean {
  return (
    protocol === "openai" ||
    protocol === "azure" ||
    protocol === "anthropic" ||
    protocol === "anthropic-messages"
  )
}
