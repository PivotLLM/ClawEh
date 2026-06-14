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

interface CallbackConfig {
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
  callback?: CallbackConfig | null
  temperature?: number
  summarization_models?: string[]
  share_common?: boolean
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
  const cbRaw = asRecord(r.callback)
  const cbMins = asNumber(cbRaw.window_minutes)
  return {
    id: asString(r.id),
    name: asString(r.name) || undefined,
    enabled: enabledRaw === false ? false : true,
    default: r.default === true,
    models: asArray(r.models).map(asString).filter(Boolean),
    skills: asArray(r.skills).map(asString).filter(Boolean),
    tools: asArray(r.tools).map(asString).filter(Boolean),
    callback: cbMins > 0 ? { window_minutes: cbMins, window_count: asNumber(cbRaw.window_count) || 2 } : null,
    temperature: typeof r.temperature === "number" ? r.temperature : undefined,
    summarization_models: asArray(r.summarization_models).map(asString).filter(Boolean),
    share_common: r.share_common === false ? false : true,
  }
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
  callbackWindowMinutes?: number
  callbackWindowCount?: number
  temperature?: number
  summarizationModels?: string[]
  shareCommon?: boolean
  onToggleEnabled?: () => void
  onModelsChange: (models: string[]) => void
  onSkillsChange: (skills: string[]) => void
  onToolsChange: (tools: string[]) => void
  onCallbackChange?: (mins: number, count: number) => void
  onTemperatureChange?: (t: number | undefined) => void
  onSummarizationModelsChange?: (models: string[]) => void
  onShareCommonChange?: (share: boolean) => void
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
  callbackWindowMinutes = 0,
  callbackWindowCount = 2,
  temperature = undefined,
  summarizationModels = [],
  shareCommon = true,
  onToggleEnabled,
  onModelsChange,
  onSkillsChange,
  onToolsChange,
  onCallbackChange,
  onTemperatureChange = undefined,
  onSummarizationModelsChange = undefined,
  onShareCommonChange = undefined,
  onDelete,
  status,
}: AgentCardProps) {
  const { t } = useTranslation()
  const [toolsExpanded, setToolsExpanded] = useState(false)

  return (
    <div className="border-border/60 bg-card rounded-xl border p-4 space-y-3">
      <div className="flex items-center justify-between gap-2">
        <div>
          <span className="font-mono text-sm font-semibold">{name || label}</span>
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
        <p className="text-muted-foreground text-xs font-medium">Models (tried in order)</p>
        <FallbacksSelect
          fallbacks={selectedModels}
          primary=""
          models={models}
          onChange={onModelsChange}
        />
      </div>

      {onSummarizationModelsChange !== undefined && (
        <div className="space-y-1.5">
          <p className="text-muted-foreground text-xs font-medium">
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
          <p className="text-muted-foreground text-xs font-medium">Skills</p>
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
            <span className={`text-xs font-medium ${tools.length === 0 ? "text-amber-400" : "text-muted-foreground"}`}>
              Tools ({tools.length === 0 ? "none — no tool access" : `${tools.includes("*") ? "all" : tools.length} granted`})
            </span>
          </button>
          {toolsExpanded && (
            <ToolSelect
              selected={tools}
              catalog={availableTools}
              onChange={onToolsChange}
            />
          )}
        </div>
      )}

      {onCallbackChange !== undefined && (
        <div className="space-y-1.5">
          <p className="text-muted-foreground text-xs font-medium">Callback token</p>
          <div className="flex items-center gap-2">
            <Input
              type="number"
              min={0}
              value={callbackWindowMinutes}
              onChange={(e) => onCallbackChange(Math.max(0, parseInt(e.target.value) || 0), callbackWindowCount)}
              className="w-20 h-7 text-xs"
            />
            <span className="text-muted-foreground text-xs">min window (0 = disabled)</span>
          </div>
          {callbackWindowMinutes > 0 && (
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={1}
                value={callbackWindowCount}
                onChange={(e) => onCallbackChange(callbackWindowMinutes, Math.max(1, parseInt(e.target.value) || 1))}
                className="w-20 h-7 text-xs"
              />
              <span className="text-muted-foreground text-xs">windows retained</span>
            </div>
          )}
          {callbackWindowMinutes > 0 && (
            <p className="text-muted-foreground text-xs">
              Token valid for {callbackWindowMinutes * callbackWindowCount} min. Endpoint:{" "}
              <span className="font-mono">POST /api/reply/&#123;token&#125;</span>
            </p>
          )}
        </div>
      )}

      {onTemperatureChange !== undefined && (
        <div className="space-y-1.5">
          <p className="text-muted-foreground text-xs font-medium">Temperature</p>
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
            <p className="text-muted-foreground text-xs font-medium">
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
        callback: a.callback && a.callback.window_minutes > 0
          ? { window_minutes: a.callback.window_minutes, window_count: a.callback.window_count }
          : null,
        ...(a.temperature !== undefined ? { temperature: a.temperature } : {}),
        ...(a.summarization_models && a.summarization_models.length > 0
          ? { summarization_models: a.summarization_models }
          : {}),
        ...(a.share_common === false ? { share_common: false } : {}),
      })),
    },
  })

  const handleSaveAgent = async (index: number, models: string[], skills: string[], tools: string[], callbackMins: number, callbackCount: number, temperature: number | undefined, summarizationModels: string[], shareCommon: boolean) => {
    const list = [...(agentsCfg.list ?? [])]
    list[index] = {
      ...list[index],
      models: models.length > 0 ? models : undefined,
      skills: skills.length > 0 ? skills : undefined,
      tools: tools,
      callback: callbackMins > 0 ? { window_minutes: callbackMins, window_count: callbackCount } : null,
      temperature,
      summarization_models: summarizationModels.length > 0 ? summarizationModels : undefined,
      share_common: shareCommon,
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

  const handleAddAgent = async () => {
    if (!addingId.trim()) {
      toast.error("Agent ID is required")
      return
    }
    const list = sortAgentList([
      ...(agentsCfg.list ?? []),
      {
        id: addingId.trim(),
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
  const [agentCallbackEdits, setAgentCallbackEdits] = useState<Array<{ mins: number; count: number }>>([])
  const [agentTemperatureEdits, setAgentTemperatureEdits] = useState<Array<number | undefined>>([])
  const [agentSummarizationEdits, setAgentSummarizationEdits] = useState<string[][]>([])
  const [agentShareCommonEdits, setAgentShareCommonEdits] = useState<boolean[]>([])
  useEffect(() => {
    if (skipAgentsResync.current) {
      skipAgentsResync.current = false
      return
    }
    setAgentModelsEdits((agentsCfg.list ?? []).map((a) => a.models ?? []))
    setAgentSkillsEdits((agentsCfg.list ?? []).map((a) => a.skills ?? []))
    setAgentToolsEdits((agentsCfg.list ?? []).map((a) => a.tools ?? []))
    setAgentCallbackEdits((agentsCfg.list ?? []).map((a) => ({
      mins: a.callback?.window_minutes ?? 0,
      count: a.callback?.window_count ?? 2,
    })))
    setAgentTemperatureEdits((agentsCfg.list ?? []).map((a) => a.temperature))
    setAgentSummarizationEdits((agentsCfg.list ?? []).map((a) => a.summarization_models ?? []))
    setAgentShareCommonEdits((agentsCfg.list ?? []).map((a) => a.share_common !== false))
  }, [agentsCfg.list])

  // Mirror the latest edit values into a ref so the debounced autosave fires
  // with current data rather than the values captured when the timer was set.
  const latestRef = useRef({
    agentModelsEdits,
    agentSkillsEdits,
    agentToolsEdits,
    agentCallbackEdits,
    agentTemperatureEdits,
    agentSummarizationEdits,
    agentShareCommonEdits,
  })
  latestRef.current = {
    agentModelsEdits,
    agentSkillsEdits,
    agentToolsEdits,
    agentCallbackEdits,
    agentTemperatureEdits,
    agentSummarizationEdits,
    agentShareCommonEdits,
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
        L.agentCallbackEdits[index]?.mins ?? 0,
        L.agentCallbackEdits[index]?.count ?? 2,
        L.agentTemperatureEdits[index],
        L.agentSummarizationEdits[index] ?? [],
        L.agentShareCommonEdits[index] ?? true,
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
              {/* Named agents. Agent defaults (default agent, default model,
                  summarization models) now live on the Config page. */}
              {(agentsCfg.list ?? []).map((agent, i) => (
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
                  callbackWindowMinutes={agentCallbackEdits[i]?.mins ?? 0}
                  callbackWindowCount={agentCallbackEdits[i]?.count ?? 2}
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
                  onCallbackChange={(mins, count) => {
                    setAgentCallbackEdits((prev) => {
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
                      <p className="text-muted-foreground text-xs font-medium">Models (tried in order)</p>
                      <FallbacksSelect
                        fallbacks={addingModels}
                        primary=""
                        models={models}
                        onChange={setAddingModels}
                      />
                    </div>
                    {availableSkills.length > 0 && (
                      <div className="space-y-1.5">
                        <p className="text-muted-foreground text-xs font-medium">Skills</p>
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
                          <span className={`text-xs font-medium ${addingTools.length === 0 ? "text-amber-400" : "text-muted-foreground"}`}>
                            Tools ({addingTools.length === 0 ? "none — no tool access" : `${addingTools.includes("*") ? "all" : addingTools.length} granted`})
                          </span>
                        </button>
                        {addingToolsExpanded && (
                          <ToolSelect
                            selected={addingTools}
                            catalog={availableTools}
                            onChange={setAddingTools}
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
  )
}
