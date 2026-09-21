// Types, parsing and small helpers for the agents page.
//
// Split out of agents-page.tsx, which had grown to 1619 lines: this is the part
// with no React in it at all, and it is the part the card, the rail and the
// autosave hook all need. Same shape as config/form-model.ts and
// mcp/form-model.ts.

export interface MessageConfig {
  window_minutes: number
  window_count: number
}

export interface AgentEntry {
  id: string
  name?: string
  enabled?: boolean
  default?: boolean
  models?: string[]
  skills?: string[]
  tools?: string[]
  message?: MessageConfig | null
  temperature?: number
  /** Days an `event` memory is kept. undefined = agents.defaults; 0 = the
   *  built-in default; negative = keep forever. */
  event_retention_days?: number
  /** Days a retired memory is kept after it was retired. Same convention. */
  retired_retention_days?: number
  summarization_models?: string[]
  share_common?: boolean
  global_cron?: boolean
  maestro?: MaestroSettings
  fusion?: boolean
  cogmem?: boolean
  mounts?: MountEntry[]
  mcp_tools?: string[]
}

export interface MountEntry {
  name: string
  path: string
  notify?: boolean
  writable?: boolean
}

export interface AgentsConfig {
  defaults: {
    models?: string[]
    temperature?: number
  }
  list?: AgentEntry[]
}

export interface SkillInfo {
  name: string
  description?: string
  source?: string
}

export function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>
  }
  return {}
}

export function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

export function asString(value: unknown): string {
  return typeof value === "string" ? value : ""
}

export function asNumber(value: unknown, defaultVal = 0): number {
  return typeof value === "number" ? value : defaultVal
}

// splitCsv parses a comma-separated MCP-allow string into trimmed, non-empty entries.
export function splitCsv(s: string): string[] {
  return s
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean)
}

// MCPAccessServer is one row of the MCP access checkbox list.
export interface MCPAccessServer {
  name: string
  checked: boolean
  // configured is false for an entry that names no configured server (a
  // server since removed, or a hand-typed entry from before the checkbox
  // list); it is shown checked and flagged so it can be removed.
  configured: boolean
}

// mcpAccessView turns an agent's mcp_tools entries into checkbox rows: one
// per configured server (checked when an entry matches it, case-insensitively)
// followed by one flagged row per entry that matches no configured server.
// Access is per server; there is no finer grant.
export function mcpAccessView(
  entries: string[],
  serverNames: string[],
): MCPAccessServer[] {
  const norm = (s: string) => s.trim().toLowerCase()
  const rows: MCPAccessServer[] = serverNames.map((name) => ({
    name,
    checked: entries.some((e) => norm(e) === norm(name)),
    configured: true,
  }))
  for (const raw of entries) {
    const e = raw.trim()
    if (e && !serverNames.some((n) => norm(n) === norm(e))) {
      rows.push({ name: e, checked: true, configured: false })
    }
  }
  return rows
}

// mcpAccessEntries is the inverse of mcpAccessView: the mcp_tools list for
// the checked rows.
export function mcpAccessEntries(rows: MCPAccessServer[]): string[] {
  return rows.filter((s) => s.checked).map((s) => s.name)
}

// settingsCardClass groups a set of agent settings into one bordered card.
export const settingsCardClass =
  "border-border/60 bg-card rounded-xl border p-4 space-y-5"

export function parseAgent(value: unknown): AgentEntry {
  const r = asRecord(value)
  const enabledRaw = r.enabled
  const cbRaw = asRecord(r.message)
  const cbMins = asNumber(cbRaw.window_minutes)
  return {
    id: asString(r.id),
    name: asString(r.name) || undefined,
    enabled: enabledRaw === false ? false : true,
    default: r.default === true,
    models: asArray(r.models).map(asString).filter(Boolean),
    skills: asArray(r.skills).map(asString).filter(Boolean),
    // Drop any stale mcp_* entries from the per-tool allowlist: MCP access now
    // lives in mcp_tools, so saving an edited agent cleanly migrates it off the
    // old all-or-nothing wildcard.
    tools: asArray(r.tools)
      .map(asString)
      .filter(Boolean)
      .filter((tName) => !tName.toLowerCase().startsWith("mcp_")),
    mcp_tools: asArray(r.mcp_tools).map(asString).filter(Boolean),
    message:
      cbMins > 0
        ? {
            window_minutes: cbMins,
            window_count: asNumber(cbRaw.window_count) || 2,
          }
        : null,
    temperature: typeof r.temperature === "number" ? r.temperature : undefined,
    event_retention_days:
      typeof r.event_retention_days === "number"
        ? r.event_retention_days
        : undefined,
    retired_retention_days:
      typeof r.retired_retention_days === "number"
        ? r.retired_retention_days
        : undefined,
    summarization_models: asArray(r.summarization_models)
      .map(asString)
      .filter(Boolean),
    share_common: r.share_common === false ? false : true,
    global_cron: r.global_cron === true,
    maestro: maestroFromRaw(r.maestro),
    fusion: r.fusion === true,
    cogmem: r.cogmem !== false,
    mounts: asArray(r.mounts).map((m) => {
      const mr = asRecord(m)
      return {
        name: asString(mr.name),
        path: asString(mr.path),
        notify: mr.notify === true,
        writable: mr.writable === true,
      }
    }),
  }
}

// AgentBindingView is a read-only projection of one binding for the Channels
// display. The raw binding objects are preserved separately for saving so that
// fields this page doesn't model (account_id, guild_id, …) are never dropped.
export interface AgentBindingView {
  index: number // index into the full bindings array
  channel: string
  peerKind: string
  peerID: string
  isDefault: boolean
  hasPeer: boolean // routing peer present → delivers there, no chat id needed
  deliverTo: string // explicit cron delivery chat id (for peerless channels)
}

export function parseAgentBindings(
  appConfig: unknown,
): Record<string, unknown>[] {
  return asArray(asRecord(appConfig).bindings).map((b) => asRecord(b))
}

export function bindingViewsForAgent(
  raw: Record<string, unknown>[],
  agentID: string,
): AgentBindingView[] {
  const views: AgentBindingView[] = []
  raw.forEach((b, index) => {
    if (asString(b.agent_id) !== agentID) return
    const match = asRecord(b.match)
    const peer = asRecord(match.peer)
    const channel = asString(match.channel)
    const peerKind = asString(peer.kind)
    const peerID = asString(peer.id)
    views.push({
      index,
      channel,
      peerKind,
      peerID,
      isDefault: b.default === true,
      hasPeer: channel !== "" && peerKind !== "" && peerID !== "",
      deliverTo: asString(b.deliver_to),
    })
  })
  return views
}

// sortAgentList orders agents alphabetically by display name (name, falling back
// to id), case-insensitively. Order in agents.list is not semantically
// significant (the default agent is marked by its `default` flag, bindings route
// by id), so sorting for display is safe and keeps the list stable.
export function sortAgentList(list: AgentEntry[]): AgentEntry[] {
  return [...list].sort((a, b) =>
    (a.name || a.id).localeCompare(b.name || b.id, undefined, {
      sensitivity: "base",
    }),
  )
}

export function parseAgentsConfig(appConfig: unknown): AgentsConfig {
  const cfg = asRecord(appConfig)
  const agents = asRecord(cfg.agents)
  const defaults = asRecord(agents.defaults)
  return {
    defaults: {
      models: asArray(defaults.models).map(asString).filter(Boolean),
      temperature:
        typeof defaults.temperature === "number"
          ? defaults.temperature
          : undefined,
    },
    list: sortAgentList(asArray(agents.list).map(parseAgent)),
  }
}

export async function fetchSkills(): Promise<SkillInfo[]> {
  const res = await fetch("/api/skills")
  if (!res.ok) return []
  const data = (await res.json()) as { skills?: SkillInfo[] }
  return data.skills ?? []
}

/** The per-agent `maestro` config block. Absent means Maestro is off. */
export interface MaestroSettings {
  enabled: boolean
  /** Parallel task-set concurrency cap; undefined = Maestro's default (5). */
  max_concurrent?: number
  /** Dispatch rate limit: requests per period; undefined = defaults (10 / 60 s). */
  rate_limit_requests?: number
  rate_limit_period?: number
  /** Whether a parallel run may be honoured when the LLM asks; undefined = allowed. */
  allow_parallel?: boolean
}

/** The editable runner settings inside the block (the enabled switch is
 *  saved immediately like the other suite toggles, these are debounced). */
export interface MaestroRunnerEdits {
  maxConcurrent: number | undefined
  rateLimitRequests: number | undefined
  rateLimitPeriod: number | undefined
  allowParallel: boolean
}

function asPositiveInt(v: unknown): number | undefined {
  return typeof v === "number" && Number.isInteger(v) && v > 0 ? v : undefined
}

/** maestroFromRaw parses the saved block. The retired boolean form
 *  (`"maestro": true`) is not honoured by the backend, so it reads as a
 *  disabled block here too; null/undefined means no block. */
export function maestroFromRaw(v: unknown): MaestroSettings | undefined {
  if (v === null || v === undefined) return undefined
  if (typeof v !== "object" || Array.isArray(v)) return { enabled: false }
  const r = v as Record<string, unknown>
  return {
    enabled: r.enabled === true,
    max_concurrent: asPositiveInt(r.max_concurrent),
    rate_limit_requests: asPositiveInt(r.rate_limit_requests),
    rate_limit_period: asPositiveInt(r.rate_limit_period),
    allow_parallel: r.allow_parallel === false ? false : undefined,
  }
}

/** maestroPayload is the block as sent to the backend: `enabled` always, the
 *  other keys only when set, and `allow_parallel` only when false (true is
 *  the default and is left implicit). */
export function maestroPayload(m: MaestroSettings): Record<string, unknown> {
  return {
    enabled: m.enabled === true,
    ...(m.max_concurrent !== undefined
      ? { max_concurrent: m.max_concurrent }
      : {}),
    ...(m.rate_limit_requests !== undefined
      ? { rate_limit_requests: m.rate_limit_requests }
      : {}),
    ...(m.rate_limit_period !== undefined
      ? { rate_limit_period: m.rate_limit_period }
      : {}),
    ...(m.allow_parallel === false ? { allow_parallel: false } : {}),
  }
}

export function maestroEditsFromAgent(a: AgentEntry): MaestroRunnerEdits {
  return {
    maxConcurrent: a.maestro?.max_concurrent,
    rateLimitRequests: a.maestro?.rate_limit_requests,
    rateLimitPeriod: a.maestro?.rate_limit_period,
    allowParallel: a.maestro?.allow_parallel !== false,
  }
}

/** applyMaestroEdits folds the debounced runner edits back into the saved
 *  block; an agent without a block stays without one. */
export function applyMaestroEdits(
  m: MaestroSettings | undefined,
  e: MaestroRunnerEdits,
): MaestroSettings | undefined {
  if (!m) return undefined
  return {
    enabled: m.enabled,
    max_concurrent: e.maxConcurrent,
    rate_limit_requests: e.rateLimitRequests,
    rate_limit_period: e.rateLimitPeriod,
    allow_parallel: e.allowParallel ? undefined : false,
  }
}
