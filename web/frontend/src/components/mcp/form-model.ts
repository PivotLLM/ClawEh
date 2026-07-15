export type JsonRecord = Record<string, unknown>

export interface MCPHostForm {
  enabled: boolean
  autoEnable: boolean
  listen: string
  endpointPath: string
  // Per-endpoint catalogue visibility filters (mcp_host.internal_tools /
  // external_tools). Empty ⇒ expose all; entries match by MatchVisibility.
  internalToolPatterns: string[]
  externalToolPatterns: string[]
  // Progressive tool discovery. discoveryEnabled is the global switch
  // (tools.discovery.enabled). alwaysShownNamespaces are EXTRA namespaces kept in
  // tools/list when it's on (search_tools/get_tool_details and cogmem are always
  // shown by rule).
  discoveryEnabled: boolean
  // ttlMax: turns a revealed tool stays visible without use (reset on use).
  // visibleBudget: max revealed tools before lowest-TTL-first pruning kicks in.
  ttlMax: number
  visibleBudget: number
  alwaysShownNamespaces: string[]
  // Outbound MCP client resilience (tools.mcp.*). reconnectCooldownSeconds and
  // callTimeoutSeconds fall back to backend defaults (30 / 300) when 0;
  // livenessProbeSeconds of 0 disables background probing.
  reconnectCooldownSeconds: number
  callTimeoutSeconds: number
  livenessProbeSeconds: number
  // External (upstream) MCP servers claw connects out to (tools.mcp.servers),
  // structured for add/edit/delete in the UI.
  servers: MCPServerForm[]
}

// MCPServerForm is one external server, edited via form fields (no raw JSON).
export interface MCPServerForm {
  name: string
  enabled: boolean
  type: "stdio" | "http" // "sse" is treated as http (deprecated alias)
  // stdio
  command: string
  args: string // one arg per line
  env: string // KEY=VALUE per line
  envFile: string
  // http
  url: string
  headers: string // "Header: value" per line
  // Under progressive discovery, reveal all of this server's tools as soon as one
  // is discovered (for small, cohesive servers). Ignored when discovery is off.
  revealTogether: boolean
}

export function blankServer(): MCPServerForm {
  return {
    name: "",
    enabled: true,
    type: "http",
    command: "",
    args: "",
    env: "",
    envFile: "",
    url: "",
    headers: "",
    revealTogether: false,
  }
}

export const EMPTY_MCP_FORM: MCPHostForm = {
  enabled: false,
  autoEnable: true,
  listen: "127.0.0.1:5911",
  endpointPath: "/mcp",
  internalToolPatterns: ["*"],
  externalToolPatterns: ["*"],
  discoveryEnabled: false,
  ttlMax: 50,
  visibleBudget: 100,
  alwaysShownNamespaces: [],
  reconnectCooldownSeconds: 30,
  callTimeoutSeconds: 300,
  livenessProbeSeconds: 0,
  servers: [],
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

function asNumber(value: unknown, fallback: number): number {
  if (typeof value === "number" && Number.isFinite(value)) return value
  return fallback
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
  const discovery = asRecord(asRecord(root.tools).discovery)
  const mcpClient = asRecord(asRecord(root.tools).mcp)
  return {
    enabled: asBool(mcp.enabled, EMPTY_MCP_FORM.enabled),
    autoEnable: asBool(mcp.auto_enable, EMPTY_MCP_FORM.autoEnable),
    listen: asString(mcp.listen, EMPTY_MCP_FORM.listen),
    endpointPath: asString(mcp.endpoint_path, EMPTY_MCP_FORM.endpointPath),
    internalToolPatterns: asStringArray(mcp.internal_tools, EMPTY_MCP_FORM.internalToolPatterns),
    externalToolPatterns: asStringArray(mcp.external_tools, EMPTY_MCP_FORM.externalToolPatterns),
    discoveryEnabled: asBool(discovery.enabled, EMPTY_MCP_FORM.discoveryEnabled),
    // Honor the legacy "ttl" key as a fallback so an older config still displays.
    ttlMax: asNumber(discovery.ttl_max ?? discovery.ttl, EMPTY_MCP_FORM.ttlMax),
    visibleBudget: asNumber(discovery.visible_budget, EMPTY_MCP_FORM.visibleBudget),
    alwaysShownNamespaces: asStringArray(mcp.always_shown_namespaces, EMPTY_MCP_FORM.alwaysShownNamespaces),
    reconnectCooldownSeconds: asNumber(
      mcpClient.reconnect_cooldown_seconds,
      EMPTY_MCP_FORM.reconnectCooldownSeconds,
    ),
    callTimeoutSeconds: asNumber(mcpClient.call_timeout_seconds, EMPTY_MCP_FORM.callTimeoutSeconds),
    livenessProbeSeconds: asNumber(
      mcpClient.liveness_probe_seconds,
      EMPTY_MCP_FORM.livenessProbeSeconds,
    ),
    servers: serversFromConfig(config),
  }
}

// --- external server (tools.mcp.servers) structured conversions ---------------

function recordToLines(v: unknown, sep: string): string {
  const r = asRecord(v)
  return Object.entries(r)
    .map(([k, val]) => `${k}${sep}${typeof val === "string" ? val : String(val)}`)
    .join("\n")
}

function linesToRecord(s: string, sep: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of s.split("\n")) {
    const t = line.trim()
    if (t === "") continue
    const i = t.indexOf(sep)
    if (i < 0) continue
    const k = t.slice(0, i).trim()
    if (k !== "") out[k] = t.slice(i + sep.length).trim()
  }
  return out
}

function linesToArray(s: string): string[] {
  return s
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "")
}

export function serversFromConfig(config: unknown): MCPServerForm[] {
  const servers = asRecord(asRecord(asRecord(config).tools).mcp).servers
  const rec = asRecord(servers)
  const out: MCPServerForm[] = []
  for (const name of Object.keys(rec).sort()) {
    const s = asRecord(rec[name])
    const type: "stdio" | "http" =
      s.type === "stdio" || (!s.type && s.command && !s.url) ? "stdio" : "http"
    out.push({
      name,
      enabled: asBool(s.enabled, false),
      type,
      command: asString(s.command, ""),
      args: Array.isArray(s.args)
        ? (s.args as unknown[]).filter((a): a is string => typeof a === "string").join("\n")
        : "",
      env: recordToLines(s.env, "="),
      envFile: asString(s.env_file, ""),
      url: asString(s.url, ""),
      headers: recordToLines(s.headers, ": "),
      revealTogether: asBool(s.reveal_together, false),
    })
  }
  return out
}

// validateServers returns the first problem found, or null when valid.
export function validateServers(servers: MCPServerForm[]): string | null {
  const seen = new Set<string>()
  for (const s of servers) {
    const name = s.name.trim()
    if (name === "") return "Each external server needs a name."
    if (seen.has(name)) return `Duplicate server name: ${name}`
    seen.add(name)
    if (s.type === "stdio" && s.command.trim() === "") {
      return `Server "${name}": command is required for stdio.`
    }
    if (s.type === "http" && s.url.trim() === "") {
      return `Server "${name}": URL is required for http.`
    }
  }
  return null
}

// serversToPatch builds the tools.mcp.servers patch: present names overwrite,
// names dropped since baseline are set to null so the backend deletes them.
export function serversToPatch(
  servers: MCPServerForm[],
  baseline: MCPServerForm[],
): Record<string, unknown> {
  const patch: Record<string, unknown> = {}
  const seen = new Set<string>()
  for (const s of servers) {
    const name = s.name.trim()
    if (name === "") continue
    seen.add(name)
    const cfg: Record<string, unknown> = { enabled: s.enabled, type: s.type }
    if (s.type === "stdio") {
      cfg.command = s.command.trim()
      cfg.args = linesToArray(s.args)
      const env = linesToRecord(s.env, "=")
      if (Object.keys(env).length > 0) cfg.env = env
      if (s.envFile.trim() !== "") cfg.env_file = s.envFile.trim()
    } else {
      cfg.url = s.url.trim()
      const headers = linesToRecord(s.headers, ":")
      if (Object.keys(headers).length > 0) cfg.headers = headers
    }
    if (s.revealTogether) cfg.reveal_together = true
    patch[name] = cfg
  }
  for (const s of baseline) {
    const name = s.name.trim()
    if (name !== "" && !seen.has(name)) patch[name] = null
  }
  return patch
}

// matchToolPattern mirrors pkg/config.MatchToolPattern: "*" matches all,
// entries ending in "*" are case-insensitive prefix matches, otherwise
// case-insensitive exact match.
// matchVisibility mirrors pkg/config.MatchVisibility: collapse underscore runs,
// strip a leading mcp_, then an entry matches by equality-or-prefix. "*" = all;
// a trailing glob is tolerated ("fusion_*" == "fusion_").
export function matchVisibility(patterns: string[], name: string): boolean {
  if (!patterns || patterns.length === 0) return false
  const bare = name.toLowerCase().replace(/_+/g, "_").replace(/^mcp_/, "")
  for (const raw of patterns) {
    let e = raw.trim().toLowerCase()
    if (e === "") continue
    if (e === "*") return true
    e = e.replace(/\*+$/, "").replace(/_+/g, "_")
    if (e === "") continue
    if (bare.startsWith(e)) return true
  }
  return false
}

// MCPHost no longer hard-excludes any tool; every tool obeys the allowlist
// (msg_send included). Kept as an empty list so the preview UI can stay generic.
export const ALWAYS_EXCLUDED_TOOLS: string[] = []

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
