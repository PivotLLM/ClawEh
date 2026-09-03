import { IconChevronRight, IconLoader2, IconPlus } from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type AgentToolCatalogResponse,
  getAgentTools,
  getAppConfig,
  patchAppConfig,
} from "@/api/channels"
import { type ModelInfo, getModels } from "@/api/models"
import { AgentCard } from "@/components/agents/agent-card"
import {
  type AgentsConfig,
  type SkillInfo,
  asString,
  bindingViewsForAgent,
  fetchSkills,
  parseAgentBindings,
  parseAgentsConfig,
  sortAgentList,
} from "@/components/agents/agent-model"
import { FallbacksSelect } from "@/components/agents/model-selects"
import { SkillsSelect } from "@/components/agents/skills-select"
import { ToolSelect } from "@/components/agents/tool-select"
import {
  type AgentEdits,
  editsFromAgent,
  useAgentAutosave,
} from "@/components/agents/use-agent-autosave"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

// Stable empties for the pending state: a fresh [] on every render would be a
// new identity and would invalidate every memo that depends on these.
const EMPTY_MODELS: ModelInfo[] = []
const EMPTY_SKILLS: SkillInfo[] = []
const EMPTY_TOOLS: AgentToolCatalogResponse = { tools: [], default_tools: [] }
const EMPTY_BINDINGS: Record<string, unknown>[] = []

export function AgentsPage() {
  const queryClient = useQueryClient()
  const { t } = useTranslation()
  const [agentsCfg, setAgentsCfg] = useState<AgentsConfig>({
    defaults: {},
    list: [],
  })
  const [saving, setSaving] = useState<string | null>(null)
  // Raw binding objects, preserved verbatim so saving never drops fields this
  // page doesn't model. Only the per-agent `default` flag is edited here.

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

  const {
    data: loaded,
    isPending: loading,
    error: loadError,
  } = useQuery({
    queryKey: ["agents-page"],
    queryFn: async () => {
      const [appConfig, modelsData, skillsData, toolsData] = await Promise.all([
        getAppConfig(),
        getModels(),
        fetchSkills(),
        getAgentTools(),
      ])
      return {
        agentsCfg: parseAgentsConfig(appConfig),
        bindings: parseAgentBindings(appConfig),
        models: modelsData.models,
        availableSkills: [...skillsData].sort((a, b) =>
          a.name.localeCompare(b.name),
        ),
        availableTools: toolsData,
      }
    },
  })

  const fetchError = loadError
    ? loadError instanceof Error
      ? loadError.message
      : "Failed to load"
    : ""

  // Everything except agentsCfg is read-only here, so it is derived straight
  // from the query rather than mirrored into state. agentsCfg keeps its state
  // because the edit flow below writes to it optimistically.
  const models = loaded?.models ?? EMPTY_MODELS
  const availableSkills = loaded?.availableSkills ?? EMPTY_SKILLS
  const availableTools = loaded?.availableTools ?? EMPTY_TOOLS
  const bindings = loaded?.bindings ?? EMPTY_BINDINGS

  // Seed the editable config when a fetch lands. Adjusted during render rather
  // than in an effect so the page is never painted with an empty agent list for
  // a frame, and it fires only for a genuinely new fetch result.
  const [syncedLoad, setSyncedLoad] = useState(loaded)
  if (loaded && loaded !== syncedLoad) {
    setSyncedLoad(loaded)
    setAgentsCfg(loaded.agentsCfg)
  }

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
        message:
          a.message && a.message.window_minutes > 0
            ? {
                window_minutes: a.message.window_minutes,
                window_count: a.message.window_count,
              }
            : null,
        ...(a.temperature !== undefined ? { temperature: a.temperature } : {}),
        ...(a.summarization_models && a.summarization_models.length > 0
          ? { summarization_models: a.summarization_models }
          : {}),
        ...(a.share_common === false ? { share_common: false } : {}),
        ...(a.global_cron ? { global_cron: true } : {}),
        ...(a.maestro ? { maestro: true } : {}),
        ...(a.fusion ? { fusion: true } : {}),
        ...(a.cogmem === false ? { cogmem: false } : {}),
        // Always sent (like tools/mounts) so clearing the box persists; the
        // backend drops an empty slice on save (omitempty).
        mcp_tools: a.mcp_tools ?? [],
        // Always sent (like tools) so removing all mounts persists; the backend
        // drops an empty slice on save (omitempty).
        mounts: (a.mounts ?? [])
          .filter((m) => m.name.trim() !== "" && m.path.trim() !== "")
          .map((m) => ({
            name: m.name.trim(),
            path: m.path.trim(),
            ...(m.notify ? { notify: true } : {}),
            ...(m.writable ? { writable: true } : {}),
          })),
      })),
    },
  })

  const handleSaveAgent = async (index: number, edits: AgentEdits) => {
    const list = [...(agentsCfg.list ?? [])]
    list[index] = {
      ...list[index],
      models: edits.models.length > 0 ? edits.models : undefined,
      skills: edits.skills.length > 0 ? edits.skills : undefined,
      tools: edits.tools,
      message:
        edits.message.mins > 0
          ? {
              window_minutes: edits.message.mins,
              window_count: edits.message.count,
            }
          : null,
      temperature: edits.temperature,
      summarization_models:
        edits.summarizationModels.length > 0
          ? edits.summarizationModels
          : undefined,
      share_common: edits.shareCommon,
      mounts: edits.mounts,
      mcp_tools: edits.mcpTools,
    }
    const next: AgentsConfig = { ...agentsCfg, list }
    try {
      await patchAppConfig(buildPayload(next))
      // In-place update: no reload, so no scroll jump. The hook suppresses its
      // reseed around this write, so the saved snapshot cannot overwrite a
      // field that is still being edited.
      setAgentsCfg(next)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save")
      throw e // the hook turns a rejection into the card's error state
    }
  }

  // The edit buffers, their debounce and the per-card save hint all live in
  // the hook; the page keeps only what it takes to turn one buffer into a
  // config patch.
  const autosave = useAgentAutosave(agentsCfg.list ?? [], handleSaveAgent)

  const edit = autosave.editAgent

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

  // Independent toggle: flip the agent's Fusion tool suite on/off.
  const handleToggleFusion = async (index: number) => {
    const list = [...(agentsCfg.list ?? [])]
    list[index] = { ...list[index], fusion: !list[index].fusion }
    const next: AgentsConfig = { ...agentsCfg, list }
    setSaving(`fusion-${index}`)
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
  const handleSetDefaultBinding = async (
    agentID: string,
    targetIndex: number,
    deliverTo?: string,
  ) => {
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
      // Optimistic cache update rather than a refetch, matching what the old
      // setBindings(next) did: the patch already succeeded, so re-reading the
      // whole page would only cost a round trip.
      queryClient.setQueryData(["agents-page"], (prev: typeof loaded) =>
        prev ? { ...prev, bindings: next } : prev,
      )
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : "Failed to save default channel",
      )
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

  // Keep the rail selection valid: default to the first agent on load, and
  // recover when the selected agent is removed. Derived during render rather
  // than written back through an effect — an effect renders once with a stale
  // or empty selection and then again with the corrected one, and everything
  // below keys off this value.
  const agentList = agentsCfg.list ?? []
  const activeId =
    agentList.length === 0
      ? ""
      : agentList.some((a) => a.id === selectedId)
        ? selectedId
        : agentList[0].id

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

      <div className="flex min-h-0 flex-1">
        {/* Left rail: one entry per agent. Selecting one renders just its card,
            so the page no longer scrolls through every agent at once. */}
        {!loading && !fetchError && (agentsCfg.list ?? []).length > 0 && (
          <nav className="border-border/60 w-52 shrink-0 space-y-0.5 overflow-y-auto border-r px-2 py-4">
            {(agentsCfg.list ?? []).map((agent) => {
              const active = !showAdd && agent.id === activeId
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
          <div className="w-full max-w-250 space-y-3 pt-4">
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
                  .map((agent, i) => ({
                    agent,
                    i,
                    e: autosave.edits[i] ?? editsFromAgent(agent),
                  }))
                  .filter(({ agent }) => !showAdd && agent.id === activeId)
                  .map(({ agent, i, e }) => (
                    <AgentCard
                      key={agent.id}
                      label={agent.id}
                      name={agent.name}
                      enabled={agent.enabled !== false}
                      selectedModels={e.models}
                      skills={e.skills}
                      tools={e.tools}
                      availableSkills={availableSkills}
                      availableTools={availableTools}
                      models={models}
                      messageWindowMinutes={e.message.mins}
                      messageWindowCount={e.message.count}
                      temperature={e.temperature}
                      onToggleEnabled={() => handleToggleAgent(i)}
                      onModelsChange={(m) => edit(i, { models: m })}
                      onSkillsChange={(sk) => edit(i, { skills: sk })}
                      onToolsChange={(tl) => edit(i, { tools: tl })}
                      onMessageChange={(mins, count) =>
                        edit(i, { message: { mins, count } })
                      }
                      onTemperatureChange={(tp) => edit(i, { temperature: tp })}
                      summarizationModels={e.summarizationModels}
                      onSummarizationModelsChange={(sm) =>
                        edit(i, { summarizationModels: sm })
                      }
                      shareCommon={e.shareCommon}
                      onShareCommonChange={(sc) => edit(i, { shareCommon: sc })}
                      globalCron={agent.global_cron === true}
                      onGlobalCronChange={() => handleToggleGlobalCron(i)}
                      maestro={agent.maestro === true}
                      onMaestroChange={() => handleToggleMaestro(i)}
                      fusion={agent.fusion === true}
                      onFusionChange={() => handleToggleFusion(i)}
                      cogmem={agent.cogmem !== false}
                      onCogmemChange={() => handleToggleCogmem(i)}
                      mounts={e.mounts}
                      onMountsChange={(ms) => edit(i, { mounts: ms })}
                      mcpTools={e.mcpTools}
                      onMCPToolsChange={(mt) => edit(i, { mcpTools: mt })}
                      agentBindings={bindingViewsForAgent(bindings, agent.id)}
                      onSetDefaultBinding={(target, deliverTo) =>
                        handleSetDefaultBinding(agent.id, target, deliverTo)
                      }
                      onDelete={() => handleDeleteAgent(i)}
                      status={autosave.status[`agent-${i}`]}
                    />
                  ))}

                {/* Add agent form */}
                {showAdd && (
                  <div className="border-border/60 bg-card space-y-3 rounded-xl border p-4">
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
                        <p className="text-foreground text-sm font-semibold">
                          Models (tried in order)
                        </p>
                        <FallbacksSelect
                          fallbacks={addingModels}
                          primary=""
                          models={models}
                          onChange={setAddingModels}
                        />
                      </div>
                      {availableSkills.length > 0 && (
                        <div className="space-y-1.5">
                          <p className="text-foreground text-sm font-semibold">
                            Skills
                          </p>
                          <SkillsSelect
                            selected={addingSkills}
                            availableSkills={availableSkills}
                            onChange={setAddingSkills}
                          />
                        </div>
                      )}
                      {(availableTools.tools.length > 0 ||
                        (availableTools.mcp_servers?.length ?? 0) > 0) && (
                        <div className="space-y-1.5">
                          <button
                            type="button"
                            onClick={() => setAddingToolsExpanded((v) => !v)}
                            className="flex cursor-pointer items-center gap-1 select-none"
                          >
                            <IconChevronRight
                              className={`text-muted-foreground size-3.5 opacity-60 transition-transform duration-200 ${addingToolsExpanded ? "rotate-90" : ""}`}
                            />
                            <span
                              className={`text-sm font-semibold ${addingTools.length === 0 ? "text-amber-400" : "text-foreground"}`}
                            >
                              Always-On Tools (
                              {addingTools.length === 0
                                ? "none — no tool access"
                                : `${addingTools.includes("*") ? "all" : addingTools.length} granted`}
                              )
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
                      <Button
                        onClick={handleAddAgent}
                        disabled={saving === "add"}
                      >
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
