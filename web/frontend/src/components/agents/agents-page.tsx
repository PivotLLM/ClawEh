import { IconChevronRight, IconLoader2, IconPlus, IconTrash } from "@tabler/icons-react"
import { Switch } from "@/components/ui/switch"
import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { type ModelInfo, getModels } from "@/api/models"
import { type AgentToolCatalogResponse, getAppConfig, getAgentTools, patchAppConfig } from "@/api/channels"
import { FallbacksSelect } from "@/components/agents/model-selects"
import { ToolSelect } from "@/components/agents/tool-select"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

interface MessageConfig {
  window_minutes: number
  window_count: number
}

interface AgentEntry {
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
  cogmem?: boolean
  mounts?: MountEntry[]
}

interface MountEntry {
  name: string
  path: string
  notify?: boolean
}

interface AgentsConfig {
  defaults: {
    models?: string[]
    temperature?: number
  }
  list?: AgentEntry[]
}

interface SkillInfo {
  name: string
  description?: string
  source?: string
}

function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>
  }
  return {}
}

function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function asNumber(value: unknown, defaultVal = 0): number {
  return typeof value === "number" ? value : defaultVal
}

function parseAgent(value: unknown): AgentEntry {
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
    tools: asArray(r.tools).map(asString).filter(Boolean),
    message: cbMins > 0 ? { window_minutes: cbMins, window_count: asNumber(cbRaw.window_count) || 2 } : null,
    temperature: typeof r.temperature === "number" ? r.temperature : undefined,
    summarization_models: asArray(r.summarization_models).map(asString).filter(Boolean),
    share_common: r.share_common === false ? false : true,
    global_cron: r.global_cron === true,
    maestro: r.maestro === true,
    cogmem: r.cogmem !== false,
    mounts: asArray(r.mounts).map((m) => {
      const mr = asRecord(m)
      return { name: asString(mr.name), path: asString(mr.path), notify: mr.notify === true }
    }),
  }
}

// AgentBindingView is a read-only projection of one binding for the Channels
// display. The raw binding objects are preserved separately for saving so that
// fields this page doesn't model (account_id, guild_id, …) are never dropped.
interface AgentBindingView {
  index: number // index into the full bindings array
  channel: string
  peerKind: string
  peerID: string
  isDefault: boolean
  hasPeer: boolean // routing peer present → delivers there, no chat id needed
  deliverTo: string // explicit cron delivery chat id (for peerless channels)
}

function parseAgentBindings(appConfig: unknown): Record<string, unknown>[] {
  return asArray(asRecord(appConfig).bindings).map((b) => asRecord(b))
}

function bindingViewsForAgent(raw: Record<string, unknown>[], agentID: string): AgentBindingView[] {
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
function sortAgentList(list: AgentEntry[]): AgentEntry[] {
  return [...list].sort((a, b) =>
    (a.name || a.id).localeCompare(b.name || b.id, undefined, { sensitivity: "base" }),
  )
}

function parseAgentsConfig(appConfig: unknown): AgentsConfig {
  const cfg = asRecord(appConfig)
  const agents = asRecord(cfg.agents)
  const defaults = asRecord(agents.defaults)
  return {
    defaults: {
      models: asArray(defaults.models).map(asString).filter(Boolean),
      temperature: typeof defaults.temperature === "number" ? defaults.temperature : undefined,
    },
    list: sortAgentList(asArray(agents.list).map(parseAgent)),
  }
}

async function fetchSkills(): Promise<SkillInfo[]> {
  const res = await fetch("/api/skills")
  if (!res.ok) return []
  const data = (await res.json()) as { skills?: SkillInfo[] }
  return data.skills ?? []
}

interface SkillsSelectProps {
  selected: string[]
  availableSkills: SkillInfo[]
  onChange: (skills: string[]) => void
}

function SkillsSelect({ selected, availableSkills, onChange }: SkillsSelectProps) {
  const isAllSelected = selected.length === 0
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-1.5">
        {availableSkills.map((skill) => {
          const active = selected.includes(skill.name)
          return (
            <button
              key={skill.name}
              type="button"
              onClick={() => {
                if (active) {
                  onChange(selected.filter((s) => s !== skill.name))
                } else {
                  onChange([...selected, skill.name])
                }
              }}
              className={[
                "rounded-md border px-2 py-0.5 text-xs font-medium transition-colors cursor-pointer",
                active
                  ? "border-primary/50 bg-secondary text-foreground"
                  : "border-border/50 bg-transparent text-muted-foreground hover:border-border hover:text-foreground",
              ].join(" ")}
              title={skill.description}
            >
              {skill.name}
            </button>
          )
        })}
        {availableSkills.length === 0 && (
          <span className="text-muted-foreground text-xs">No skills installed</span>
        )}
      </div>
      {availableSkills.length > 0 && (
        <p className="text-muted-foreground text-xs">
          {isAllSelected
            ? "No skills selected (agent has no skill access)"
            : `${selected.length} skill${selected.length === 1 ? "" : "s"} selected`}
        </p>
      )}
    </div>
  )
}

interface AgentCardProps {
  label: string
  name?: string
  enabled?: boolean
  selectedModels: string[]
  skills: string[]
  tools: string[]
  availableSkills: SkillInfo[]
  availableTools: AgentToolCatalogResponse
  models: ModelInfo[]
  messageWindowMinutes?: number
  messageWindowCount?: number
  temperature?: number
  summarizationModels?: string[]
  shareCommon?: boolean
  globalCron?: boolean
  maestro?: boolean
  cogmem?: boolean
  mounts?: MountEntry[]
  onMountsChange?: (mounts: MountEntry[]) => void
  agentBindings?: AgentBindingView[]
  onSetDefaultBinding?: (targetIndex: number, deliverTo?: string) => void
  onToggleEnabled?: () => void
  onModelsChange: (models: string[]) => void
  onSkillsChange: (skills: string[]) => void
  onToolsChange: (tools: string[]) => void
  onMessageChange?: (mins: number, count: number) => void
  onTemperatureChange?: (t: number | undefined) => void
  onSummarizationModelsChange?: (models: string[]) => void
  onShareCommonChange?: (share: boolean) => void
  onGlobalCronChange?: (v: boolean) => void
  onMaestroChange?: (v: boolean) => void
  onCogmemChange?: (v: boolean) => void
  onDelete?: () => void
  status?: "saving" | "saved" | "error"
}

function AgentCard({
  label,
  name,
  enabled,
  selectedModels,
  skills,
  tools,
  availableSkills,
  availableTools,
  models,
  messageWindowMinutes = 0,
  messageWindowCount = 2,
  temperature = undefined,
  summarizationModels = [],
  shareCommon = true,
  globalCron = false,
  maestro = false,
  cogmem = true,
  mounts = [],
  onMountsChange = undefined,
  agentBindings = [],
  onSetDefaultBinding = undefined,
  onToggleEnabled,
  onModelsChange,
  onSkillsChange,
  onToolsChange,
  onMessageChange,
  onTemperatureChange = undefined,
  onSummarizationModelsChange = undefined,
  onShareCommonChange = undefined,
  onGlobalCronChange = undefined,
  onMaestroChange = undefined,
  onCogmemChange = undefined,
  onDelete,
  status,
}: AgentCardProps) {
  const { t } = useTranslation()
  const [toolsExpanded, setToolsExpanded] = useState(false)
  // Local edits for explicit cron chat ids (peerless channels), keyed by the
  // binding's index in the full bindings array.
  const [deliverEdits, setDeliverEdits] = useState<Record<number, string>>({})
  const deliverValue = (b: AgentBindingView) => deliverEdits[b.index] ?? b.deliverTo

  return (
    <div className="border-border/60 bg-card rounded-xl border p-4 space-y-5">
      <div className="flex items-center justify-between gap-2">
        <div>
          <span className="font-mono text-lg font-semibold">{name || label}</span>
          {name && name !== label && (
            <span className="text-muted-foreground font-mono text-xs ml-2">({label})</span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {status && (
            <span
              className={`text-xs ${status === "error" ? "text-destructive" : status === "saved" ? "text-emerald-500" : "text-muted-foreground"}`}
            >
              {status === "saving" ? "Saving…" : status === "saved" ? "Saved ✓" : "Save failed"}
            </span>
          )}
          {onToggleEnabled !== undefined && (
            <Switch
              checked={enabled ?? true}
              onCheckedChange={onToggleEnabled}
              aria-label={(enabled ?? true) ? "Disable agent" : "Enable agent"}
            />
          )}
          {onDelete && (
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={onDelete}
              className="text-muted-foreground hover:text-destructive hover:bg-destructive/10"
            >
              <IconTrash className="size-3.5" />
            </Button>
          )}
        </div>
      </div>

      <div className="space-y-1.5">
        <p className="text-foreground text-sm font-semibold">Models (tried in order)</p>
        <FallbacksSelect
          fallbacks={selectedModels}
          primary=""
          models={models}
          onChange={onModelsChange}
        />
      </div>

      {onSummarizationModelsChange !== undefined && (
        <div className="space-y-1.5">
          <p className="text-foreground text-sm font-semibold">
            {t("agents.summarizationModels")}
          </p>
          <FallbacksSelect
            fallbacks={summarizationModels}
            primary=""
            models={models}
            onChange={onSummarizationModelsChange}
            addPlaceholder={t("agents.summarizationModelsAdd")}
          />
          <p className="text-muted-foreground text-xs">
            {t("agents.summarizationModelsHint")}
          </p>
        </div>
      )}

      {availableSkills.length > 0 && (
        <div className="space-y-1.5">
          <p className="text-foreground text-sm font-semibold">Skills</p>
          <SkillsSelect
            selected={skills}
            availableSkills={availableSkills}
            onChange={onSkillsChange}
          />
        </div>
      )}

      {(availableTools.tools.length > 0 || (availableTools.mcp_servers?.length ?? 0) > 0) && (
        <div className="space-y-1.5">
          <button
            type="button"
            onClick={() => setToolsExpanded((v) => !v)}
            className="flex items-center gap-1 cursor-pointer select-none"
          >
            <IconChevronRight
              className={`size-3.5 text-muted-foreground opacity-60 transition-transform duration-200 ${toolsExpanded ? "rotate-90" : ""}`}
            />
            <span className={`text-sm font-semibold ${tools.length === 0 ? "text-amber-400" : "text-foreground"}`}>
              Tools ({tools.length === 0 ? "none — no tool access" : `${tools.includes("*") ? "all" : tools.length} granted`})
            </span>
          </button>
          {toolsExpanded && (
            <ToolSelect
              selected={tools}
              catalog={availableTools}
              onChange={onToolsChange}
              suiteStates={{ maestro, cogmem }}
            />
          )}
        </div>
      )}

      {onMountsChange !== undefined && (
        <div className="space-y-1.5">
          <p className="text-foreground text-sm font-semibold">
            Mounts (external folders, beside files/)
          </p>
          {mounts.map((m, mi) => {
            const set = (patch: Partial<MountEntry>) =>
              onMountsChange(mounts.map((x, j) => (j === mi ? { ...x, ...patch } : x)))
            return (
              <div key={mi} className="flex items-center gap-1.5">
                <Input
                  value={m.name}
                  onChange={(e) => set({ name: e.target.value })}
                  placeholder="name (e.g. notes)"
                  className="h-7 w-32 font-mono text-xs"
                />
                <Input
                  value={m.path}
                  onChange={(e) => set({ path: e.target.value })}
                  placeholder="/absolute/path"
                  className="h-7 flex-1 font-mono text-xs"
                />
                <label className="flex items-center gap-1 text-xs text-muted-foreground select-none">
                  <Switch
                    checked={m.notify === true}
                    onCheckedChange={(c) => set({ notify: c })}
                  />
                  notify
                </label>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="h-7 w-7"
                  aria-label="remove mount"
                  onClick={() => onMountsChange(mounts.filter((_, j) => j !== mi))}
                >
                  <IconTrash className="size-3.5" />
                </Button>
              </div>
            )
          })}
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-6 text-xs px-2"
            onClick={() => onMountsChange([...mounts, { name: "", path: "", notify: false }])}
          >
            <IconPlus className="size-3.5" />
            Add mount
          </Button>
        </div>
      )}

      {onMessageChange !== undefined && (
        <div className="space-y-1.5">
          <p className="text-foreground text-sm font-semibold">External message token</p>
          <div className="flex items-center gap-2">
            <Input
              type="number"
              min={0}
              value={messageWindowMinutes}
              onChange={(e) => onMessageChange(Math.max(0, parseInt(e.target.value) || 0), messageWindowCount)}
              className="w-20 h-7 text-xs"
            />
            <span className="text-muted-foreground text-xs">min window (0 = disabled)</span>
          </div>
          {messageWindowMinutes > 0 && (
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={1}
                value={messageWindowCount}
                onChange={(e) => onMessageChange(messageWindowMinutes, Math.max(1, parseInt(e.target.value) || 1))}
                className="w-20 h-7 text-xs"
              />
              <span className="text-muted-foreground text-xs">windows retained</span>
            </div>
          )}
          {messageWindowMinutes > 0 && (
            <p className="text-muted-foreground text-xs">
              Token valid for {messageWindowMinutes * messageWindowCount} min. Endpoint:{" "}
              <span className="font-mono">POST /api/message/&#123;token&#125;</span>
            </p>
          )}
        </div>
      )}

      {onTemperatureChange !== undefined && (
        <div className="space-y-1.5">
          <p className="text-foreground text-sm font-semibold">Temperature</p>
          <div className="flex items-center gap-2">
            <Input
              type="number"
              min={0}
              max={2}
              step={0.1}
              value={temperature ?? ""}
              onChange={(e) => {
                const v = e.target.value
                onTemperatureChange(v === "" ? undefined : parseFloat(v))
              }}
              className="w-20 h-7 text-xs"
              placeholder="default"
            />
            <span className="text-muted-foreground text-xs">(0–2, blank = use default)</span>
          </div>
        </div>
      )}

      {onShareCommonChange !== undefined && (
        <div className="space-y-1.5">
          <div className="flex items-center justify-between gap-2">
            <p className="text-foreground text-sm font-semibold">
              {t("agents.shareCommon")}
            </p>
            <Switch
              checked={shareCommon}
              onCheckedChange={onShareCommonChange}
              aria-label={t("agents.shareCommon")}
            />
          </div>
          <p className="text-muted-foreground text-xs">
            {t("agents.shareCommonHint")}
          </p>
        </div>
      )}

      {onCogmemChange !== undefined && (
        <div className="space-y-1.5">
          <div className="flex items-center justify-between gap-2">
            <p className="text-foreground text-sm font-semibold">
              {t("agents.cogmem")}
            </p>
            <Switch
              checked={cogmem}
              onCheckedChange={onCogmemChange}
              aria-label={t("agents.cogmem")}
            />
          </div>
          <p className="text-muted-foreground text-xs">
            {t("agents.cogmemHint")}
          </p>
        </div>
      )}

      {onMaestroChange !== undefined && (
        <div className="space-y-1.5">
          <div className="flex items-center justify-between gap-2">
            <p className="text-foreground text-sm font-semibold">
              {t("agents.maestro")}
            </p>
            <Switch
              checked={maestro}
              onCheckedChange={onMaestroChange}
              aria-label={t("agents.maestro")}
            />
          </div>
          <p className="text-muted-foreground text-xs">
            {t("agents.maestroHint")}
          </p>
        </div>
      )}

      {onGlobalCronChange !== undefined && (
        <div className="space-y-1.5">
          <div className="flex items-center justify-between gap-2">
            <p className="text-foreground text-sm font-semibold">
              {t("agents.globalCron")}
            </p>
            <Switch
              checked={globalCron}
              onCheckedChange={onGlobalCronChange}
              aria-label={t("agents.globalCron")}
            />
          </div>
          <p className="text-muted-foreground text-xs">
            {t("agents.globalCronHint")}
          </p>
        </div>
      )}

      {onSetDefaultBinding !== undefined && (
        <div className="space-y-1.5">
          <p className="text-foreground text-sm font-semibold">
            {t("agents.channels")}
          </p>
          {agentBindings.length === 0 ? (
            <p className="text-muted-foreground text-xs">{t("agents.channelsNone")}</p>
          ) : (
            <div className="space-y-1.5">
              {agentBindings.map((b) => (
                <div key={b.index} className="flex items-center gap-2 text-xs">
                  <input
                    type="radio"
                    name={`default-channel-${label}`}
                    checked={b.isDefault}
                    onChange={() => {
                      if (b.hasPeer) {
                        onSetDefaultBinding(b.index)
                        return
                      }
                      const to = deliverValue(b).trim()
                      if (!to) {
                        toast.error(t("agents.channelsNeedChatId"))
                        return
                      }
                      onSetDefaultBinding(b.index, to)
                    }}
                  />
                  <span className="font-mono">
                    {b.channel}
                    {b.hasPeer ? ` · ${b.peerKind}:${b.peerID}` : ""}
                  </span>
                  {!b.hasPeer && (
                    <input
                      type="text"
                      className="border-border/60 bg-background w-28 rounded border px-1.5 py-0.5 font-mono text-xs"
                      placeholder={t("agents.channelsChatIdPlaceholder")}
                      value={deliverValue(b)}
                      onChange={(e) =>
                        setDeliverEdits((s) => ({ ...s, [b.index]: e.target.value }))
                      }
                      onBlur={() => {
                        const to = deliverValue(b).trim()
                        if (b.isDefault && to) onSetDefaultBinding(b.index, to)
                      }}
                    />
                  )}
                  {b.isDefault && (
                    <span className="text-muted-foreground">— {t("agents.channelsDefault")}</span>
                  )}
                </div>
              ))}
            </div>
          )}
          <p className="text-muted-foreground text-xs">{t("agents.channelsHint")}</p>
        </div>
      )}

    </div>
  )
}

export function AgentsPage() {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(true)
  const [fetchError, setFetchError] = useState("")
  const [models, setModels] = useState<ModelInfo[]>([])
  const [availableSkills, setAvailableSkills] = useState<SkillInfo[]>([])
  const [availableTools, setAvailableTools] = useState<AgentToolCatalogResponse>({ tools: [], default_tools: [] })
  const [agentsCfg, setAgentsCfg] = useState<AgentsConfig>({
    defaults: {},
    list: [],
  })
  const [saving, setSaving] = useState<string | null>(null)
  // Raw binding objects, preserved verbatim so saving never drops fields this
  // page doesn't model. Only the per-agent `default` flag is edited here.
  const [bindings, setBindings] = useState<Record<string, unknown>[]>([])

  // Autosave plumbing. autoStatus drives the per-card "Saving…/Saved" hint.
  // The skip refs suppress the buffer-resync effects when WE caused the
  // agentsCfg change (a field autosave), so an in-flight edit is never clobbered
  // by the saved snapshot; add/delete (which change the agent set) still resync.
  const [autoStatus, setAutoStatus] = useState<
    Record<string, "saving" | "saved" | "error">
  >({})
  const skipAgentsResync = useRef(false)
  const saveTimers = useRef<Record<string, ReturnType<typeof setTimeout>>>({})
  const savedTimers = useRef<Record<string, ReturnType<typeof setTimeout>>>({})

  const markSaved = useCallback((key: string) => {
    setAutoStatus((s) => ({ ...s, [key]: "saved" }))
    clearTimeout(savedTimers.current[key])
    savedTimers.current[key] = setTimeout(() => {
      setAutoStatus((s) => {
        const next = { ...s }
        delete next[key]
        return next
      })
    }, 2000)
  }, [])

  // For adding new agent
  const [addingId, setAddingId] = useState("")
  const [addingName, setAddingName] = useState("")
  const [addingModels, setAddingModels] = useState<string[]>([])
  const [addingSkills, setAddingSkills] = useState<string[]>([])
  const [addingTools, setAddingTools] = useState<string[]>([])
  const [addingToolsExpanded, setAddingToolsExpanded] = useState(false)
  const [showAdd, setShowAdd] = useState(false)
  // Which agent the left rail has selected; only that agent's card is rendered.
  const [selectedId, setSelectedId] = useState("")

  const loadData = useCallback(async () => {
    setLoading(true)
    try {
      const [appConfig, modelsData, skillsData, toolsData] = await Promise.all([
        getAppConfig(),
        getModels(),
        fetchSkills(),
        getAgentTools(),
      ])
      setAgentsCfg(parseAgentsConfig(appConfig))
      setBindings(parseAgentBindings(appConfig))
      setModels(modelsData.models)
      setAvailableSkills([...skillsData].sort((a, b) => a.name.localeCompare(b.name)))
      setAvailableTools(toolsData)
      setFetchError("")
    } catch (e) {
      setFetchError(e instanceof Error ? e.message : "Failed to load")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadData()
  }, [loadData])

  // Note: agents.defaults (default model/temperature) and the default-agent
  // selector live on the Config page now, so this payload intentionally omits
  // `defaults`. The backend patch is a deep merge, so leaving it out preserves
  // whatever the Config page last saved.
  const buildPayload = (cfg: AgentsConfig) => ({
    agents: {
      list: (cfg.list ?? []).map((a) => ({
        id: a.id,
        ...(a.enabled === false ? { enabled: false } : {}),
        ...(a.name ? { name: a.name } : {}),
        ...(a.default ? { default: true } : {}),
        ...(a.models && a.models.length > 0 ? { models: a.models } : {}),
        ...(a.skills && a.skills.length > 0 ? { skills: a.skills } : {}),
        tools: a.tools ?? [],
        message: a.message && a.message.window_minutes > 0
          ? { window_minutes: a.message.window_minutes, window_count: a.message.window_count }
          : null,
        ...(a.temperature !== undefined ? { temperature: a.temperature } : {}),
        ...(a.summarization_models && a.summarization_models.length > 0
          ? { summarization_models: a.summarization_models }
          : {}),
        ...(a.share_common === false ? { share_common: false } : {}),
        ...(a.global_cron ? { global_cron: true } : {}),
        ...(a.maestro ? { maestro: true } : {}),
        ...(a.cogmem === false ? { cogmem: false } : {}),
        // Always sent (like tools) so removing all mounts persists; the backend
        // drops an empty slice on save (omitempty).
        mounts: (a.mounts ?? [])
          .filter((m) => m.name.trim() !== "" && m.path.trim() !== "")
          .map((m) => ({
            name: m.name.trim(),
            path: m.path.trim(),
            ...(m.notify ? { notify: true } : {}),
          })),
      })),
    },
  })

  const handleSaveAgent = async (index: number, models: string[], skills: string[], tools: string[], messageMins: number, messageCount: number, temperature: number | undefined, summarizationModels: string[], shareCommon: boolean, mounts: MountEntry[]) => {
    const list = [...(agentsCfg.list ?? [])]
    list[index] = {
      ...list[index],
      models: models.length > 0 ? models : undefined,
      skills: skills.length > 0 ? skills : undefined,
      tools: tools,
      message: messageMins > 0 ? { window_minutes: messageMins, window_count: messageCount } : null,
      temperature,
      summarization_models: summarizationModels.length > 0 ? summarizationModels : undefined,
      share_common: shareCommon,
      mounts,
    }
    const next: AgentsConfig = { ...agentsCfg, list }
    const key = `agent-${index}`
    setAutoStatus((s) => ({ ...s, [key]: "saving" }))
    try {
      await patchAppConfig(buildPayload(next))
      // In-place update (no reload → no scroll jump); skip the resync so the
      // saved snapshot doesn't overwrite a field that's still being edited.
      skipAgentsResync.current = true
      setAgentsCfg(next)
      markSaved(key)
    } catch (e) {
      setAutoStatus((s) => ({ ...s, [key]: "error" }))
      toast.error(e instanceof Error ? e.message : "Failed to save")
    }
  }

  const handleDeleteAgent = async (index: number) => {
    setSaving(`delete-${index}`)
    const list = (agentsCfg.list ?? []).filter((_, i) => i !== index)
    const next: AgentsConfig = { ...agentsCfg, list }
    try {
      await patchAppConfig(buildPayload(next))
      toast.success("Deleted")
      // Update local state in place instead of reloading the whole page, which
      // would unmount the list and scroll back to the top.
      setAgentsCfg(next)
      // Move the rail selection to a neighbour rather than letting it reset to
      // the top.
      if (list.length > 0) {
        setSelectedId(list[Math.min(index, list.length - 1)].id)
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to delete")
    } finally {
      setSaving(null)
    }
  }

  const handleToggleAgent = async (index: number) => {
    const list = [...(agentsCfg.list ?? [])]
    const current = list[index]
    list[index] = { ...current, enabled: !current.enabled }
    const next: AgentsConfig = { ...agentsCfg, list }
    setSaving(`toggle-${index}`)
    try {
      await patchAppConfig(buildPayload(next))
      // Update local state in place instead of reloading the whole page, which
      // would unmount the list and scroll back to the top.
      setAgentsCfg(next)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save")
    } finally {
      setSaving(null)
    }
  }

  // Independent toggle (not threaded through the big autosave): flip global_cron.
  const handleToggleGlobalCron = async (index: number) => {
    const list = [...(agentsCfg.list ?? [])]
    list[index] = { ...list[index], global_cron: !list[index].global_cron }
    const next: AgentsConfig = { ...agentsCfg, list }
    setSaving(`globalcron-${index}`)
    try {
      await patchAppConfig(buildPayload(next))
      setAgentsCfg(next)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save")
    } finally {
      setSaving(null)
    }
  }

  // Independent toggle: flip the agent's Maestro tool suite on/off.
  const handleToggleMaestro = async (index: number) => {
    const list = [...(agentsCfg.list ?? [])]
    list[index] = { ...list[index], maestro: !list[index].maestro }
    const next: AgentsConfig = { ...agentsCfg, list }
    setSaving(`maestro-${index}`)
    try {
      await patchAppConfig(buildPayload(next))
      setAgentsCfg(next)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save")
    } finally {
      setSaving(null)
    }
  }

  // Independent toggle: flip the agent's cognitive-memory suite on/off (default on).
  const handleToggleCogmem = async (index: number) => {
    const list = [...(agentsCfg.list ?? [])]
    list[index] = { ...list[index], cogmem: !(list[index].cogmem !== false) }
    const next: AgentsConfig = { ...agentsCfg, list }
    setSaving(`cogmem-${index}`)
    try {
      await patchAppConfig(buildPayload(next))
      setAgentsCfg(next)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save")
    } finally {
      setSaving(null)
    }
  }

  // Set the given binding (by its index in the full bindings array) as the
  // agent's default channel, clearing default on the agent's other bindings.
  // deliverTo is the explicit cron chat id for channels without a routing peer
  // (e.g. a Telegram bot); it is stored on the target binding. The raw binding
  // objects are mapped in place so unknown fields are preserved.
  const handleSetDefaultBinding = async (agentID: string, targetIndex: number, deliverTo?: string) => {
    const next = bindings.map((b, i) => {
      if (asString(b.agent_id) !== agentID) return b
      if (i !== targetIndex) return { ...b, default: false }
      const updated: Record<string, unknown> = { ...b, default: true }
      if (deliverTo !== undefined) updated.deliver_to = deliverTo
      return updated
    })
    setSaving(`binding-${targetIndex}`)
    try {
      await patchAppConfig({ bindings: next })
      setBindings(next)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save default channel")
    } finally {
      setSaving(null)
    }
  }

  const handleAddAgent = async () => {
    if (!addingId.trim()) {
      toast.error("Agent ID is required")
      return
    }
    const newId = addingId.trim()
    const list = sortAgentList([
      ...(agentsCfg.list ?? []),
      {
        id: newId,
        ...(addingName.trim() ? { name: addingName.trim() } : {}),
        ...(addingModels.length > 0 ? { models: addingModels } : {}),
        skills: addingSkills.length > 0 ? addingSkills : undefined,
        tools: addingTools,
      },
    ])
    const next: AgentsConfig = { ...agentsCfg, list }
    setSaving("add")
    try {
      await patchAppConfig(buildPayload(next))
      toast.success("Agent added")
      setAddingId("")
      setAddingName("")
      setAddingModels([])
      setAddingSkills([])
      setAddingTools([])
      setShowAdd(false)
      // Update local state in place instead of reloading the whole page, which
      // would unmount the list and scroll back to the top.
      setAgentsCfg(next)
      setSelectedId(newId)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to add agent")
    } finally {
      setSaving(null)
    }
  }

  // Local edit state for each agent
  const [agentModelsEdits, setAgentModelsEdits] = useState<string[][]>([])
  const [agentSkillsEdits, setAgentSkillsEdits] = useState<string[][]>([])
  const [agentToolsEdits, setAgentToolsEdits] = useState<string[][]>([])
  const [agentMessageEdits, setAgentMessageEdits] = useState<Array<{ mins: number; count: number }>>([])
  const [agentTemperatureEdits, setAgentTemperatureEdits] = useState<Array<number | undefined>>([])
  const [agentSummarizationEdits, setAgentSummarizationEdits] = useState<string[][]>([])
  const [agentShareCommonEdits, setAgentShareCommonEdits] = useState<boolean[]>([])
  const [agentMountsEdits, setAgentMountsEdits] = useState<MountEntry[][]>([])
  useEffect(() => {
    if (skipAgentsResync.current) {
      skipAgentsResync.current = false
      return
    }
    setAgentModelsEdits((agentsCfg.list ?? []).map((a) => a.models ?? []))
    setAgentSkillsEdits((agentsCfg.list ?? []).map((a) => a.skills ?? []))
    setAgentToolsEdits((agentsCfg.list ?? []).map((a) => a.tools ?? []))
    setAgentMessageEdits((agentsCfg.list ?? []).map((a) => ({
      mins: a.message?.window_minutes ?? 0,
      count: a.message?.window_count ?? 2,
    })))
    setAgentTemperatureEdits((agentsCfg.list ?? []).map((a) => a.temperature))
    setAgentSummarizationEdits((agentsCfg.list ?? []).map((a) => a.summarization_models ?? []))
    setAgentShareCommonEdits((agentsCfg.list ?? []).map((a) => a.share_common !== false))
    setAgentMountsEdits((agentsCfg.list ?? []).map((a) => a.mounts ?? []))
  }, [agentsCfg.list])

  // Keep the rail selection valid: default to the first agent on load, and
  // recover when the selected agent is removed.
  useEffect(() => {
    const list = agentsCfg.list ?? []
    if (list.length === 0) {
      if (selectedId !== "") setSelectedId("")
      return
    }
    if (!list.some((a) => a.id === selectedId)) {
      setSelectedId(list[0].id)
    }
  }, [agentsCfg.list, selectedId])

  // Mirror the latest edit values into a ref so the debounced autosave fires
  // with current data rather than the values captured when the timer was set.
  const latestRef = useRef({
    agentModelsEdits,
    agentSkillsEdits,
    agentToolsEdits,
    agentMessageEdits,
    agentTemperatureEdits,
    agentSummarizationEdits,
    agentShareCommonEdits,
    agentMountsEdits,
  })
  latestRef.current = {
    agentModelsEdits,
    agentSkillsEdits,
    agentToolsEdits,
    agentMessageEdits,
    agentTemperatureEdits,
    agentSummarizationEdits,
    agentShareCommonEdits,
    agentMountsEdits,
  }

  const AUTOSAVE_MS = 600
  const scheduleSaveAgent = (index: number) => {
    const key = `agent-${index}`
    clearTimeout(saveTimers.current[key])
    saveTimers.current[key] = setTimeout(() => {
      const L = latestRef.current
      void handleSaveAgent(
        index,
        L.agentModelsEdits[index] ?? [],
        L.agentSkillsEdits[index] ?? [],
        L.agentToolsEdits[index] ?? [],
        L.agentMessageEdits[index]?.mins ?? 0,
        L.agentMessageEdits[index]?.count ?? 2,
        L.agentTemperatureEdits[index],
        L.agentSummarizationEdits[index] ?? [],
        L.agentShareCommonEdits[index] ?? true,
        L.agentMountsEdits[index] ?? [],
      )
    }, AUTOSAVE_MS)
  }
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.agents")}>
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            setAddingTools([...availableTools.default_tools])
            setShowAdd(true)
          }}
          disabled={showAdd}
        >
          <IconPlus className="size-4" />
          Add Agent
        </Button>
      </PageHeader>

      <div className="min-h-0 flex flex-1">
        {/* Left rail: one entry per agent. Selecting one renders just its card,
            so the page no longer scrolls through every agent at once. */}
        {!loading && !fetchError && (agentsCfg.list ?? []).length > 0 && (
          <nav className="border-border/60 w-52 shrink-0 space-y-0.5 overflow-y-auto border-r px-2 py-4">
            {(agentsCfg.list ?? []).map((agent) => {
              const active = !showAdd && agent.id === selectedId
              return (
                <button
                  key={agent.id}
                  type="button"
                  onClick={() => {
                    setShowAdd(false)
                    setSelectedId(agent.id)
                  }}
                  className={`flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm transition-colors ${
                    active
                      ? "bg-accent text-accent-foreground font-medium"
                      : "text-muted-foreground hover:bg-accent/50"
                  }`}
                >
                  <span
                    className={`size-1.5 shrink-0 rounded-full ${agent.enabled !== false ? "bg-emerald-500" : "bg-muted-foreground/40"}`}
                  />
                  <span className="truncate">{agent.name || agent.id}</span>
                </button>
              )
            })}
          </nav>
        )}

        <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-8 sm:px-6">
          <div className="w-full max-w-250 pt-4 space-y-3">
          {loading && (
            <div className="flex items-center justify-center py-20">
              <IconLoader2 className="text-muted-foreground size-6 animate-spin" />
            </div>
          )}

          {fetchError && (
            <div className="text-destructive bg-destructive/10 rounded-lg px-4 py-3 text-sm">
              {fetchError}
            </div>
          )}

          {!loading && !fetchError && (
            <>
              {/* Agent defaults (default agent, default model, summarization
                  models) now live on the Config page. */}
              {(agentsCfg.list ?? []).length === 0 && !showAdd && (
                <p className="text-muted-foreground py-20 text-center text-sm">
                  No agents yet. Use “Add Agent” to create one.
                </p>
              )}
              {/* Only the rail-selected agent renders. The wrapper preserves the
                  original (agent, i) binding so the card props stay unchanged. */}
              {(agentsCfg.list ?? [])
                .map((agent, i) => ({ agent, i }))
                .filter(({ agent }) => !showAdd && agent.id === selectedId)
                .map(({ agent, i }) => (
                <AgentCard
                  key={agent.id}
                  label={agent.id}
                  name={agent.name}
                  enabled={agent.enabled !== false}
                  selectedModels={agentModelsEdits[i] ?? []}
                  skills={agentSkillsEdits[i] ?? []}
                  tools={agentToolsEdits[i] ?? []}
                  availableSkills={availableSkills}
                  availableTools={availableTools}
                  models={models}
                  messageWindowMinutes={agentMessageEdits[i]?.mins ?? 0}
                  messageWindowCount={agentMessageEdits[i]?.count ?? 2}
                  temperature={agentTemperatureEdits[i]}
                  onToggleEnabled={() => handleToggleAgent(i)}
                  onModelsChange={(m) => {
                    setAgentModelsEdits((prev) => {
                      const next = [...prev]
                      next[i] = m
                      return next
                    })
                    scheduleSaveAgent(i)
                  }}
                  onSkillsChange={(s) => {
                    setAgentSkillsEdits((prev) => {
                      const next = [...prev]
                      next[i] = s
                      return next
                    })
                    scheduleSaveAgent(i)
                  }}
                  onToolsChange={(tl) => {
                    setAgentToolsEdits((prev) => {
                      const next = [...prev]
                      next[i] = tl
                      return next
                    })
                    scheduleSaveAgent(i)
                  }}
                  onMessageChange={(mins, count) => {
                    setAgentMessageEdits((prev) => {
                      const next = [...prev]
                      next[i] = { mins, count }
                      return next
                    })
                    scheduleSaveAgent(i)
                  }}
                  onTemperatureChange={(tp) => {
                    setAgentTemperatureEdits((prev) => {
                      const next = [...prev]
                      next[i] = tp
                      return next
                    })
                    scheduleSaveAgent(i)
                  }}
                  summarizationModels={agentSummarizationEdits[i] ?? []}
                  onSummarizationModelsChange={(sm) => {
                    setAgentSummarizationEdits((prev) => {
                      const next = [...prev]
                      next[i] = sm
                      return next
                    })
                    scheduleSaveAgent(i)
                  }}
                  shareCommon={agentShareCommonEdits[i] ?? true}
                  onShareCommonChange={(sc) => {
                    setAgentShareCommonEdits((prev) => {
                      const next = [...prev]
                      next[i] = sc
                      return next
                    })
                    scheduleSaveAgent(i)
                  }}
                  globalCron={agent.global_cron === true}
                  onGlobalCronChange={() => handleToggleGlobalCron(i)}
                  maestro={agent.maestro === true}
                  onMaestroChange={() => handleToggleMaestro(i)}
                  cogmem={agent.cogmem !== false}
                  onCogmemChange={() => handleToggleCogmem(i)}
                  mounts={agentMountsEdits[i] ?? []}
                  onMountsChange={(ms) => {
                    setAgentMountsEdits((prev) => {
                      const next = [...prev]
                      next[i] = ms
                      return next
                    })
                    scheduleSaveAgent(i)
                  }}
                  agentBindings={bindingViewsForAgent(bindings, agent.id)}
                  onSetDefaultBinding={(target, deliverTo) => handleSetDefaultBinding(agent.id, target, deliverTo)}
                  onDelete={() => handleDeleteAgent(i)}
                  status={autoStatus[`agent-${i}`]}
                />
              ))}

              {/* Add agent form */}
              {showAdd && (
                <div className="border-border/60 bg-card rounded-xl border p-4 space-y-3">
                  <span className="text-sm font-semibold">New Agent</span>
                  <div className="space-y-2">
                    <Input
                      value={addingId}
                      onChange={(e) => setAddingId(e.target.value)}
                      placeholder="Agent ID (e.g. alice)"
                    />
                    <Input
                      value={addingName}
                      onChange={(e) => setAddingName(e.target.value)}
                      placeholder="Display name (optional, e.g. Sam)"
                    />
                    <div className="space-y-1.5">
                      <p className="text-foreground text-sm font-semibold">Models (tried in order)</p>
                      <FallbacksSelect
                        fallbacks={addingModels}
                        primary=""
                        models={models}
                        onChange={setAddingModels}
                      />
                    </div>
                    {availableSkills.length > 0 && (
                      <div className="space-y-1.5">
                        <p className="text-foreground text-sm font-semibold">Skills</p>
                        <SkillsSelect
                          selected={addingSkills}
                          availableSkills={availableSkills}
                          onChange={setAddingSkills}
                        />
                      </div>
                    )}
                    {(availableTools.tools.length > 0 || (availableTools.mcp_servers?.length ?? 0) > 0) && (
                      <div className="space-y-1.5">
                        <button
                          type="button"
                          onClick={() => setAddingToolsExpanded((v) => !v)}
                          className="flex items-center gap-1 cursor-pointer select-none"
                        >
                          <IconChevronRight
                            className={`size-3.5 text-muted-foreground opacity-60 transition-transform duration-200 ${addingToolsExpanded ? "rotate-90" : ""}`}
                          />
                          <span className={`text-sm font-semibold ${addingTools.length === 0 ? "text-amber-400" : "text-foreground"}`}>
                            Tools ({addingTools.length === 0 ? "none — no tool access" : `${addingTools.includes("*") ? "all" : addingTools.length} granted`})
                          </span>
                        </button>
                        {addingToolsExpanded && (
                          <ToolSelect
                            selected={addingTools}
                            catalog={availableTools}
                            onChange={setAddingTools}
                            suiteStates={{ maestro: false, cogmem: true }}
                          />
                        )}
                      </div>
                    )}
                  </div>
                  <div className="flex justify-end gap-2">
                    <Button
                      variant="outline"
                      onClick={() => {
                        setShowAdd(false)
                        setAddingId("")
                        setAddingName("")
                        setAddingModels([])
                        setAddingSkills([])
                        setAddingTools([])
                        setAddingToolsExpanded(false)
                      }}
                      disabled={saving === "add"}
                    >
                      Cancel
                    </Button>
                    <Button onClick={handleAddAgent} disabled={saving === "add"}>
                      {saving === "add" ? "Adding..." : "Add"}
                    </Button>
                  </div>
                </div>
              )}
            </>
          )}
          </div>
        </div>
      </div>
    </div>
  )
}
