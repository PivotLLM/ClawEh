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
  summarization_models?: string[]
  share_common?: boolean
  global_cron?: boolean
  maestro?: boolean
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
    summarization_models: asArray(r.summarization_models)
      .map(asString)
      .filter(Boolean),
    share_common: r.share_common === false ? false : true,
    global_cron: r.global_cron === true,
    maestro: r.maestro === true,
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
