export type JsonRecord = Record<string, unknown>

export interface MCPHostForm {
  enabled: boolean
  autoEnable: boolean
  listen: string
  endpointPath: string
  toolPatterns: string[]
}

export const EMPTY_MCP_FORM: MCPHostForm = {
  enabled: false,
  autoEnable: true,
  listen: "127.0.0.1:5911",
  endpointPath: "/mcp",
  toolPatterns: ["*"],
}

function asRecord(value: unknown): JsonRecord {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as JsonRecord
  }
  return {}
}

function asString(value: unknown, fallback: string): string {
  return typeof value === "string" && value !== "" ? value : fallback
}

function asBool(value: unknown, fallback: boolean): boolean {
  return typeof value === "boolean" ? value : fallback
}

function asStringArray(value: unknown, fallback: string[]): string[] {
  if (!Array.isArray(value)) return fallback
  const items = value.filter(
    (v): v is string => typeof v === "string" && v.trim() !== "",
  )
  return items.length > 0 ? items : fallback
}

export function buildMCPFormFromConfig(config: unknown): MCPHostForm {
  const root = asRecord(config)
  const mcp = asRecord(root.mcp_host)
  return {
    enabled: asBool(mcp.enabled, EMPTY_MCP_FORM.enabled),
    autoEnable: asBool(mcp.auto_enable, EMPTY_MCP_FORM.autoEnable),
    listen: asString(mcp.listen, EMPTY_MCP_FORM.listen),
    endpointPath: asString(mcp.endpoint_path, EMPTY_MCP_FORM.endpointPath),
    toolPatterns: asStringArray(mcp.tools, EMPTY_MCP_FORM.toolPatterns),
  }
}

// matchToolPattern mirrors pkg/config.MatchToolPattern: "*" matches all,
// entries ending in "*" are case-insensitive prefix matches, otherwise
// case-insensitive exact match.
export function matchToolPattern(patterns: string[], name: string): boolean {
  if (!patterns || patterns.length === 0) return false
  const lower = name.toLowerCase()
  for (const raw of patterns) {
    const p = raw.trim()
    if (p === "") continue
    if (p === "*") return true
    if (p.endsWith("*")) {
      const prefix = p.slice(0, -1).toLowerCase()
      if (lower.startsWith(prefix)) return true
      continue
    }
    if (lower === p.toLowerCase()) return true
  }
  return false
}

// MCPHost excludes the agent's outbound "message" tool unconditionally.
export const ALWAYS_EXCLUDED_TOOLS = ["message"]

export function validateListen(value: string): string | null {
  const v = value.trim()
  if (v === "") return "Listen address is required."
  const lastColon = v.lastIndexOf(":")
  if (lastColon < 0) return "Listen must be host:port (e.g. 127.0.0.1:5911)."
  const host = v.slice(0, lastColon)
  const portStr = v.slice(lastColon + 1)
  if (host === "") return "Listen must include a host."
  const port = Number(portStr)
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    return "Port must be an integer between 1 and 65535."
  }
  return null
}

export function validateEndpointPath(value: string): string | null {
  const v = value.trim()
  if (v === "") return "Endpoint path is required."
  if (!v.startsWith("/")) return "Endpoint path must start with '/'."
  return null
}
