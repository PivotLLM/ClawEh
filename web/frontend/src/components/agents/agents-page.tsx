import { IconChevronRight, IconLoader2, IconPlus, IconTrash } from "@tabler/icons-react"
import { Switch } from "@/components/ui/switch"
import { useCallback, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { type ModelInfo, getModels } from "@/api/models"
import { type AgentToolCatalogResponse, getAppConfig, getAgentTools, patchAppConfig } from "@/api/channels"
import { ToolSelect } from "@/components/agents/tool-select"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

interface AgentModelConfig {
  primary: string
  fallbacks?: string[]
}

interface CallbackConfig {
  window_minutes: number
  window_count: number
}

interface AgentEntry {
  id: string
  name?: string
  enabled?: boolean
  default?: boolean
  model?: AgentModelConfig | null
  skills?: string[]
  tools?: string[]
  callback?: CallbackConfig | null
  temperature?: number
}

interface AgentsConfig {
  defaults: {
    model?: AgentModelConfig | null
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

function parseModelConfig(value: unknown): AgentModelConfig | null {
  if (!value) return null
  if (typeof value === "string") return { primary: value, fallbacks: [] }
  const r = asRecord(value)
  const primary = asString(r.primary)
  if (!primary) return null
  return {
    primary,
    fallbacks: asArray(r.fallbacks).map(asString).filter(Boolean),
  }
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
    model: parseModelConfig(r.model),
    skills: asArray(r.skills).map(asString).filter(Boolean),
    tools: asArray(r.tools).map(asString).filter(Boolean),
    callback: cbMins > 0 ? { window_minutes: cbMins, window_count: asNumber(cbRaw.window_count) || 2 } : null,
    temperature: typeof r.temperature === "number" ? r.temperature : undefined,
  }
}

function parseAgentsConfig(appConfig: unknown): AgentsConfig {
  const cfg = asRecord(appConfig)
  const agents = asRecord(cfg.agents)
  const defaults = asRecord(agents.defaults)
  return {
    defaults: {
      model: parseModelConfig(defaults.model),
      temperature: typeof defaults.temperature === "number" ? defaults.temperature : undefined,
    },
    list: asArray(agents.list).map(parseAgent),
  }
}

async function fetchSkills(): Promise<SkillInfo[]> {
  const res = await fetch("/api/skills")
  if (!res.ok) return []
  const data = (await res.json()) as { skills?: SkillInfo[] }
  return data.skills ?? []
}

interface ModelSelectProps {
  value: string
  models: ModelInfo[]
  onChange: (v: string) => void
  placeholder?: string
}

function ModelSelect({ value, models, onChange, placeholder }: ModelSelectProps) {
  const configured = models.filter((m) => m.configured && m.enabled)
  const selectedModel = models.find((m) => m.model_name === value)
  const noToolsWarning = selectedModel?.no_tools === true
  return (
    <div className="space-y-1.5">
      <Select value={value || "__none__"} onValueChange={(v) => onChange(v === "__none__" ? "" : v)}>
        <SelectTrigger className="w-full">
          <SelectValue placeholder={placeholder ?? "Select model"} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="__none__">{placeholder ?? "No model override"}</SelectItem>
          {configured.map((m) => (
            <SelectItem key={m.index} value={m.model_name}>
              {m.model_name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {noToolsWarning && (
        <p className="text-amber-600 dark:text-amber-400 text-xs flex items-center gap-1">
          <span>&#9888;</span>
          Tools are disabled for {selectedModel.model_name}
        </p>
      )}
    </div>
  )
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

interface FallbacksSelectProps {
  fallbacks: string[]
  primary: string
  models: ModelInfo[]
  onChange: (fallbacks: string[]) => void
}

function FallbacksSelect({ fallbacks, primary, models, onChange }: FallbacksSelectProps) {
  const available = models.filter(
    (m) => m.configured && m.enabled && m.model_name !== primary,
  )

  const moveUp = (i: number) => {
    if (i === 0) return
    const next = [...fallbacks]
    ;[next[i - 1], next[i]] = [next[i], next[i - 1]]
    onChange(next)
  }

  const remove = (i: number) => {
    onChange(fallbacks.filter((_, idx) => idx !== i))
  }

  const add = (name: string) => {
    if (!name || fallbacks.includes(name)) return
    onChange([...fallbacks, name])
  }

  const remaining = available.filter((m) => !fallbacks.includes(m.model_name))

  return (
    <div className="space-y-1.5">
      {fallbacks.map((fb, i) => (
        <div key={fb} className="flex items-center gap-1.5">
          <span className="text-muted-foreground w-4 text-center text-xs">{i + 1}</span>
          <span className="border-border/50 bg-muted/40 flex-1 rounded px-2 py-1 font-mono text-xs">
            {fb}
          </span>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => moveUp(i)}
            disabled={i === 0}
            className="text-muted-foreground size-6"
            title="Move up"
          >
            ↑
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => remove(i)}
            className="text-muted-foreground hover:text-destructive size-6"
            title="Remove"
          >
            ×
          </Button>
        </div>
      ))}
      {remaining.length > 0 && (
        <Select value="" onValueChange={add}>
          <SelectTrigger className="h-7 text-xs">
            <SelectValue placeholder="Add fallback model…" />
          </SelectTrigger>
          <SelectContent>
            {remaining.map((m) => (
              <SelectItem key={m.index} value={m.model_name}>
                {m.model_name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
      {fallbacks.length === 0 && remaining.length === 0 && (
        <p className="text-muted-foreground text-xs">No other configured models available.</p>
      )}
    </div>
  )
}

interface AgentCardProps {
  label: string
  name?: string
  enabled?: boolean
  modelName: string
  fallbacks: string[]
  skills: string[]
  tools: string[]
  availableSkills: SkillInfo[]
  availableTools: AgentToolCatalogResponse
  models: ModelInfo[]
  callbackWindowMinutes?: number
  callbackWindowCount?: number
  temperature?: number
  onToggleEnabled?: () => void
  onModelChange: (v: string) => void
  onFallbacksChange: (fallbacks: string[]) => void
  onSkillsChange: (skills: string[]) => void
  onToolsChange: (tools: string[]) => void
  onCallbackChange?: (mins: number, count: number) => void
  onTemperatureChange?: (t: number | undefined) => void
  onDelete?: () => void
  saving: boolean
  onSave: () => void
}

function AgentCard({
  label,
  name,
  enabled,
  modelName,
  fallbacks,
  skills,
  tools,
  availableSkills,
  availableTools,
  models,
  callbackWindowMinutes = 0,
  callbackWindowCount = 2,
  temperature = undefined,
  onToggleEnabled,
  onModelChange,
  onFallbacksChange,
  onSkillsChange,
  onToolsChange,
  onCallbackChange,
  onTemperatureChange = undefined,
  onDelete,
  saving,
  onSave,
}: AgentCardProps) {
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
        <p className="text-muted-foreground text-xs font-medium">Model</p>
        <ModelSelect
          value={modelName}
          models={models}
          onChange={(v) => {
            onModelChange(v)
            // Remove new primary from fallbacks if present
            if (fallbacks.includes(v)) {
              onFallbacksChange(fallbacks.filter((f) => f !== v))
            }
          }}
          placeholder="Use default model"
        />
      </div>

      <div className="space-y-1.5">
        <p className="text-muted-foreground text-xs font-medium">Fallback models</p>
        <FallbacksSelect
          fallbacks={fallbacks}
          primary={modelName}
          models={models}
          onChange={onFallbacksChange}
        />
      </div>

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
              Tools ({tools.length === 0 ? "none — no tool access" : tools.includes("*") ? "all" : `${tools.length} granted`})
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

      <div className="flex justify-end">
        <Button size="sm" onClick={onSave} disabled={saving}>
          {saving ? "Saving..." : "Save"}
        </Button>
      </div>
    </div>
  )
}

export function AgentsPage() {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(true)
  const [fetchError, setFetchError] = useState("")
  const [models, setModels] = useState<ModelInfo[]>([])
  const [availableSkills, setAvailableSkills] = useState<SkillInfo[]>([])
  const [availableTools, setAvailableTools] = useState<AgentToolCatalogResponse>({ tools: [] })
  const [agentsCfg, setAgentsCfg] = useState<AgentsConfig>({
    defaults: {},
    list: [],
  })
  const [saving, setSaving] = useState<string | null>(null)

  // For adding new agent
  const [addingId, setAddingId] = useState("")
  const [addingName, setAddingName] = useState("")
  const [addingModel, setAddingModel] = useState("")
  const [addingFallbacks, setAddingFallbacks] = useState<string[]>([])
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
      setAvailableSkills(skillsData)
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

  const buildModelPayload = (primary: string, fallbacks: string[]) => {
    if (!primary) return null
    if (fallbacks.length > 0) return { primary, fallbacks }
    return primary  // simple string form when no fallbacks
  }

  const buildPayload = (cfg: AgentsConfig) => ({
    agents: {
      defaults: {
        model: buildModelPayload(
          cfg.defaults.model?.primary ?? "",
          cfg.defaults.model?.fallbacks ?? [],
        ),
        ...(cfg.defaults.temperature !== undefined ? { temperature: cfg.defaults.temperature } : {}),
      },
      list: (cfg.list ?? []).map((a) => ({
        id: a.id,
        ...(a.enabled === false ? { enabled: false } : {}),
        ...(a.name ? { name: a.name } : {}),
        ...(a.default ? { default: true } : {}),
        model: buildModelPayload(a.model?.primary ?? "", a.model?.fallbacks ?? []),
        ...(a.skills && a.skills.length > 0 ? { skills: a.skills } : {}),
        tools: a.tools ?? [],
        callback: a.callback && a.callback.window_minutes > 0
          ? { window_minutes: a.callback.window_minutes, window_count: a.callback.window_count }
          : null,
        ...(a.temperature !== undefined ? { temperature: a.temperature } : {}),
      })),
    },
  })

  const handleSetDefaultAgent = async (agentId: string) => {
    setSaving("set-default")
    const list = (agentsCfg.list ?? []).map((a) => ({
      ...a,
      default: a.id === agentId,
    }))
    const next: AgentsConfig = { ...agentsCfg, list }
    try {
      await patchAppConfig(buildPayload(next))
      toast.success("Default agent updated")
      await loadData()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save")
    } finally {
      setSaving(null)
    }
  }

  const handleSaveDefault = async (modelName: string, fallbacks: string[], temperature: number | undefined) => {
    setSaving("default")
    const next: AgentsConfig = {
      ...agentsCfg,
      defaults: {
        ...agentsCfg.defaults,
        model: modelName ? { primary: modelName, fallbacks } : null,
        temperature,
      },
    }
    try {
      await patchAppConfig(buildPayload(next))
      toast.success("Saved")
      await loadData()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save")
    } finally {
      setSaving(null)
    }
  }

  const handleSaveAgent = async (index: number, modelName: string, fallbacks: string[], skills: string[], tools: string[], callbackMins: number, callbackCount: number, temperature: number | undefined) => {
    setSaving(`agent-${index}`)
    const list = [...(agentsCfg.list ?? [])]
    list[index] = {
      ...list[index],
      model: modelName ? { primary: modelName, fallbacks } : null,
      skills: skills.length > 0 ? skills : undefined,
      tools: tools,
      callback: callbackMins > 0 ? { window_minutes: callbackMins, window_count: callbackCount } : null,
      temperature,
    }
    const next: AgentsConfig = { ...agentsCfg, list }
    try {
      await patchAppConfig(buildPayload(next))
      toast.success("Saved")
      await loadData()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save")
    } finally {
      setSaving(null)
    }
  }

  const handleDeleteAgent = async (index: number) => {
    setSaving(`delete-${index}`)
    const list = (agentsCfg.list ?? []).filter((_, i) => i !== index)
    const next: AgentsConfig = { ...agentsCfg, list }
    try {
      await patchAppConfig(buildPayload(next))
      toast.success("Deleted")
      await loadData()
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
      await loadData()
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
    const list = [
      ...(agentsCfg.list ?? []),
      {
        id: addingId.trim(),
        ...(addingName.trim() ? { name: addingName.trim() } : {}),
        model: addingModel ? { primary: addingModel, fallbacks: addingFallbacks } : null,
        skills: addingSkills.length > 0 ? addingSkills : undefined,
        tools: addingTools,
      },
    ]
    const next: AgentsConfig = { ...agentsCfg, list }
    setSaving("add")
    try {
      await patchAppConfig(buildPayload(next))
      toast.success("Agent added")
      setAddingId("")
      setAddingName("")
      setAddingModel("")
      setAddingFallbacks([])
      setAddingSkills([])
      setAddingTools([])
      setShowAdd(false)
      await loadData()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to add agent")
    } finally {
      setSaving(null)
    }
  }

  // Local edit state for default model + fallbacks + temperature
  const [defaultModelEdit, setDefaultModelEdit] = useState("")
  const [defaultFallbacksEdit, setDefaultFallbacksEdit] = useState<string[]>([])
  const [defaultTemperatureEdit, setDefaultTemperatureEdit] = useState<number | undefined>(undefined)
  useEffect(() => {
    setDefaultModelEdit(agentsCfg.defaults.model?.primary ?? "")
    setDefaultFallbacksEdit(agentsCfg.defaults.model?.fallbacks ?? [])
    setDefaultTemperatureEdit(agentsCfg.defaults.temperature)
  }, [agentsCfg.defaults])

  // Local edit state for each agent
  const [agentModelEdits, setAgentModelEdits] = useState<string[]>([])
  const [agentFallbacksEdits, setAgentFallbacksEdits] = useState<string[][]>([])
  const [agentSkillsEdits, setAgentSkillsEdits] = useState<string[][]>([])
  const [agentToolsEdits, setAgentToolsEdits] = useState<string[][]>([])
  const [agentCallbackEdits, setAgentCallbackEdits] = useState<Array<{ mins: number; count: number }>>([])
  const [agentTemperatureEdits, setAgentTemperatureEdits] = useState<Array<number | undefined>>([])
  useEffect(() => {
    setAgentModelEdits((agentsCfg.list ?? []).map((a) => a.model?.primary ?? ""))
    setAgentFallbacksEdits((agentsCfg.list ?? []).map((a) => a.model?.fallbacks ?? []))
    setAgentSkillsEdits((agentsCfg.list ?? []).map((a) => a.skills ?? []))
    setAgentToolsEdits((agentsCfg.list ?? []).map((a) => a.tools ?? []))
    setAgentCallbackEdits((agentsCfg.list ?? []).map((a) => ({
      mins: a.callback?.window_minutes ?? 0,
      count: a.callback?.window_count ?? 2,
    })))
    setAgentTemperatureEdits((agentsCfg.list ?? []).map((a) => a.temperature))
  }, [agentsCfg.list])

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.agents")}>
        <Button
          size="sm"
          variant="outline"
          onClick={() => setShowAdd(true)}
          disabled={showAdd}
        >
          <IconPlus className="size-4" />
          Add Agent
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-8 sm:px-6">
        <div className="mx-auto w-full max-w-250 pt-4 space-y-3">
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
              {/* Default agent selector */}
              {(agentsCfg.list ?? []).length > 0 && (
                <div className="border-border/60 bg-card rounded-xl border p-4 flex items-center gap-3">
                  <p className="text-sm font-medium shrink-0">Default agent</p>
                  <Select
                    value={(agentsCfg.list ?? []).find((a) => a.default)?.id ?? (agentsCfg.list?.[0]?.id ?? "")}
                    onValueChange={handleSetDefaultAgent}
                    disabled={saving === "set-default"}
                  >
                    <SelectTrigger className="w-48">
                      <SelectValue placeholder="Select default agent" />
                    </SelectTrigger>
                    <SelectContent>
                      {(agentsCfg.list ?? []).filter((a) => a.enabled !== false).map((a) => (
                        <SelectItem key={a.id} value={a.id}>{a.name || a.id}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className="text-muted-foreground text-xs">
                    Handles messages that don't match any binding
                  </p>
                </div>
              )}

              {/* Default model settings */}
              <AgentCard
                label="Agent Defaults"
                modelName={defaultModelEdit}
                fallbacks={defaultFallbacksEdit}
                skills={[]}
                tools={[]}
                availableSkills={[]}
                availableTools={{ tools: [] }}
                models={models}
                temperature={defaultTemperatureEdit}
                onModelChange={setDefaultModelEdit}
                onFallbacksChange={setDefaultFallbacksEdit}
                onSkillsChange={() => {}}
                onToolsChange={() => {}}
                onTemperatureChange={setDefaultTemperatureEdit}
                saving={saving === "default"}
                onSave={() => handleSaveDefault(defaultModelEdit, defaultFallbacksEdit, defaultTemperatureEdit)}
              />

              {/* Named agents */}
              {(agentsCfg.list ?? []).map((agent, i) => (
                <AgentCard
                  key={agent.id}
                  label={agent.id}
                  name={agent.name}
                  enabled={agent.enabled !== false}
                  modelName={agentModelEdits[i] ?? ""}
                  fallbacks={agentFallbacksEdits[i] ?? []}
                  skills={agentSkillsEdits[i] ?? []}
                  tools={agentToolsEdits[i] ?? []}
                  availableSkills={availableSkills}
                  availableTools={availableTools}
                  models={models}
                  callbackWindowMinutes={agentCallbackEdits[i]?.mins ?? 0}
                  callbackWindowCount={agentCallbackEdits[i]?.count ?? 2}
                  temperature={agentTemperatureEdits[i]}
                  onToggleEnabled={() => handleToggleAgent(i)}
                  onModelChange={(v) =>
                    setAgentModelEdits((prev) => {
                      const next = [...prev]
                      next[i] = v
                      return next
                    })
                  }
                  onFallbacksChange={(f) =>
                    setAgentFallbacksEdits((prev) => {
                      const next = [...prev]
                      next[i] = f
                      return next
                    })
                  }
                  onSkillsChange={(s) =>
                    setAgentSkillsEdits((prev) => {
                      const next = [...prev]
                      next[i] = s
                      return next
                    })
                  }
                  onToolsChange={(t) =>
                    setAgentToolsEdits((prev) => {
                      const next = [...prev]
                      next[i] = t
                      return next
                    })
                  }
                  onCallbackChange={(mins, count) =>
                    setAgentCallbackEdits((prev) => {
                      const next = [...prev]
                      next[i] = { mins, count }
                      return next
                    })
                  }
                  onTemperatureChange={(t) =>
                    setAgentTemperatureEdits((prev) => {
                      const next = [...prev]
                      next[i] = t
                      return next
                    })
                  }
                  onDelete={() => handleDeleteAgent(i)}
                  saving={saving === `agent-${i}` || saving === `delete-${i}` || saving === `toggle-${i}`}
                  onSave={() =>
                    handleSaveAgent(
                      i,
                      agentModelEdits[i] ?? "",
                      agentFallbacksEdits[i] ?? [],
                      agentSkillsEdits[i] ?? [],
                      agentToolsEdits[i] ?? [],
                      agentCallbackEdits[i]?.mins ?? 0,
                      agentCallbackEdits[i]?.count ?? 2,
                      agentTemperatureEdits[i],
                    )
                  }
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
                      <p className="text-muted-foreground text-xs font-medium">Model</p>
                      <ModelSelect
                        value={addingModel}
                        models={models}
                        onChange={(v) => {
                          setAddingModel(v)
                          if (addingFallbacks.includes(v)) {
                            setAddingFallbacks(addingFallbacks.filter((f) => f !== v))
                          }
                        }}
                        placeholder="Use default model"
                      />
                    </div>
                    <div className="space-y-1.5">
                      <p className="text-muted-foreground text-xs font-medium">Fallback models</p>
                      <FallbacksSelect
                        fallbacks={addingFallbacks}
                        primary={addingModel}
                        models={models}
                        onChange={setAddingFallbacks}
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
                            Tools ({addingTools.length === 0 ? "none — no tool access" : addingTools.includes("*") ? "all" : `${addingTools.length} granted`})
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
                        setAddingModel("")
                        setAddingFallbacks([])
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
