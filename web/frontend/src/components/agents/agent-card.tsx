import { IconPlus, IconTrash } from "@tabler/icons-react"
import { useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { type AgentToolCatalogResponse } from "@/api/channels"
import { type ModelInfo } from "@/api/models"
import {
  type AgentBindingView,
  type MountEntry,
  type SkillInfo,
  settingsCardClass,
  splitCsv,
} from "@/components/agents/agent-model"
import { MessageTokensSection } from "@/components/agents/message-tokens-section"
import { FallbacksSelect } from "@/components/agents/model-selects"
import { SkillsSelect } from "@/components/agents/skills-select"
import { ToolSelect } from "@/components/agents/tool-select"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"

export interface AgentCardProps {
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
  fusion?: boolean
  cogmem?: boolean
  mounts?: MountEntry[]
  onMountsChange?: (mounts: MountEntry[]) => void
  mcpTools?: string[]
  onMCPToolsChange?: (mcpTools: string[]) => void
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
  onFusionChange?: (v: boolean) => void
  onCogmemChange?: (v: boolean) => void
  onDelete?: () => void
  status?: "saving" | "saved" | "error"
}

export function AgentCard({
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
  fusion = false,
  cogmem = true,
  mounts = [],
  onMountsChange = undefined,
  mcpTools = [],
  onMCPToolsChange = undefined,
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
  onFusionChange = undefined,
  onCogmemChange = undefined,
  onDelete,
  status,
}: AgentCardProps) {
  const { t } = useTranslation()
  // Local edits for explicit cron chat ids (peerless channels), keyed by the
  // binding's index in the full bindings array.
  const [deliverEdits, setDeliverEdits] = useState<Record<number, string>>({})
  const deliverValue = (b: AgentBindingView) =>
    deliverEdits[b.index] ?? b.deliverTo
  // Raw text for the comma-delimited MCP-allow field, kept locally so typing
  // commas/spaces isn't fought by a parse-on-every-keystroke round-trip. Resets
  // per agent because AgentCard is keyed by agent id.
  const [mcpToolsRaw, setMcpToolsRaw] = useState(mcpTools.join(", "))
  const mcpServers = availableTools.mcp_servers ?? []

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <div>
          <span className="font-mono text-lg font-semibold">
            {name || label}
          </span>
          {name && name !== label && (
            <span className="text-muted-foreground ml-2 font-mono text-xs">
              ({label})
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {status && (
            <span
              className={`text-xs ${status === "error" ? "text-destructive" : status === "saved" ? "text-emerald-500" : "text-muted-foreground"}`}
            >
              {status === "saving"
                ? "Saving…"
                : status === "saved"
                  ? "Saved ✓"
                  : "Save failed"}
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

      <div className={settingsCardClass}>
        <div className="space-y-1.5">
          <p className="text-foreground text-sm font-semibold">
            Models (tried in order)
          </p>
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
      </div>

      <div className={settingsCardClass}>
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

        {availableTools.tools.length > 0 && (
          <div className="space-y-1.5">
            <p
              className={`text-sm font-semibold ${tools.length === 0 ? "text-amber-400" : "text-foreground"}`}
            >
              Always-On Tools (
              {tools.length === 0
                ? "none — no tool access"
                : `${tools.includes("*") ? "all" : tools.length} granted`}
              )
            </p>
            <p className="text-muted-foreground text-xs">
              Native tools that stay in this agent&apos;s context on every
              request. Suites (cogmem, maestro, fusion) and MCP access are
              controlled by their own toggles.
            </p>
            <ToolSelect
              selected={tools}
              catalog={availableTools}
              onChange={onToolsChange}
            />
          </div>
        )}

        {onMCPToolsChange !== undefined && (
          <div className="space-y-1.5">
            <p className="text-foreground text-sm font-semibold">MCP access</p>
            <Input
              value={mcpToolsRaw}
              onChange={(e) => {
                setMcpToolsRaw(e.target.value)
                onMCPToolsChange(splitCsv(e.target.value))
              }}
              placeholder="e.g. fusion, fusion_trello"
              className="h-7 font-mono text-xs"
            />
            <p className="text-muted-foreground text-xs">
              Comma-separated. Each entry grants MCP tools whose name equals or
              starts with it (case-insensitive); no mcp_ prefix or wildcard
              needed. Blank = no MCP tools.
              {mcpServers.length > 0
                ? ` Servers: ${mcpServers.map((s) => s.name).join(", ")}.`
                : ""}
            </p>
          </div>
        )}

        {onMountsChange !== undefined && (
          <div className="space-y-1.5">
            <p className="text-foreground text-sm font-semibold">
              Mounts (external folders, beside files/)
            </p>
            <p className="text-muted-foreground text-xs">
              Read-only unless <span className="font-medium">write</span> is
              enabled. Turn on <span className="font-medium">notify</span> to
              alert the agent when a new file appears.
            </p>
            {mounts.map((m, mi) => {
              const set = (patch: Partial<MountEntry>) =>
                onMountsChange(
                  mounts.map((x, j) => (j === mi ? { ...x, ...patch } : x)),
                )
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
                  <label className="text-muted-foreground flex items-center gap-1 text-xs select-none">
                    <Switch
                      checked={m.writable === true}
                      onCheckedChange={(c) => set({ writable: c })}
                    />
                    write
                  </label>
                  <label className="text-muted-foreground flex items-center gap-1 text-xs select-none">
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
                    onClick={() =>
                      onMountsChange(mounts.filter((_, j) => j !== mi))
                    }
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
              className="h-6 px-2 text-xs"
              onClick={() =>
                onMountsChange([
                  ...mounts,
                  { name: "", path: "", notify: false, writable: false },
                ])
              }
            >
              <IconPlus className="size-3.5" />
              Add mount
            </Button>
          </div>
        )}
      </div>

      <div className={settingsCardClass}>
        <MessageTokensSection agentId={label} />
      </div>

      {onMessageChange !== undefined && (
        <div className={settingsCardClass}>
          <div className="space-y-1.5">
            <p className="text-foreground text-sm font-semibold">
              Rotating Tokens
            </p>
            <p className="text-muted-foreground text-xs">
              Short-lived token the assistant can share; rotates automatically.
            </p>
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={0}
                value={messageWindowMinutes}
                onChange={(e) =>
                  onMessageChange(
                    Math.max(0, parseInt(e.target.value) || 0),
                    messageWindowCount,
                  )
                }
                className="h-7 w-20 text-xs"
              />
              <span className="text-muted-foreground text-xs">
                Token rotation (minutes, 0 = disabled)
              </span>
            </div>
            {messageWindowMinutes > 0 && (
              <div className="flex items-center gap-2">
                <Input
                  type="number"
                  min={1}
                  value={messageWindowCount}
                  onChange={(e) =>
                    onMessageChange(
                      messageWindowMinutes,
                      Math.max(1, parseInt(e.target.value) || 1),
                    )
                  }
                  className="h-7 w-20 text-xs"
                />
                <span className="text-muted-foreground text-xs">
                  Number of tokens retained
                </span>
              </div>
            )}
            {messageWindowMinutes > 0 && (
              <p className="text-muted-foreground text-xs">
                Effective token lifetime:{" "}
                {messageWindowMinutes * messageWindowCount} minutes. Endpoint:{" "}
                <span className="font-mono">
                  POST /api/message/&#123;token&#125;
                </span>
              </p>
            )}
          </div>
        </div>
      )}

      <div className={settingsCardClass}>
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
                className="h-7 w-20 text-xs"
                placeholder="default"
              />
              <span className="text-muted-foreground text-xs">
                (0–2, blank = use default)
              </span>
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

        {onFusionChange !== undefined && (
          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <p className="text-foreground text-sm font-semibold">
                {t("agents.fusion")}
              </p>
              <Switch
                checked={fusion}
                onCheckedChange={onFusionChange}
                aria-label={t("agents.fusion")}
              />
            </div>
            <p className="text-muted-foreground text-xs">
              {t("agents.fusionHint")}
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
              <p className="text-muted-foreground text-xs">
                {t("agents.channelsNone")}
              </p>
            ) : (
              <div className="space-y-1.5">
                {agentBindings.map((b) => {
                  // webui has no durable delivery address (its chat id is a
                  // per-browser session), so it cannot be a default channel.
                  const noDefault = b.channel === "webui"
                  return (
                    <div
                      key={b.index}
                      className="flex items-center gap-2 text-xs"
                    >
                      <input
                        type="radio"
                        name={`default-channel-${label}`}
                        checked={b.isDefault}
                        disabled={noDefault}
                        onChange={() => {
                          if (noDefault) return
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
                      {!b.hasPeer && !noDefault && (
                        <input
                          type="text"
                          className="border-border/60 bg-background w-28 rounded border px-1.5 py-0.5 font-mono text-xs"
                          placeholder={t("agents.channelsChatIdPlaceholder")}
                          value={deliverValue(b)}
                          onChange={(e) =>
                            setDeliverEdits((s) => ({
                              ...s,
                              [b.index]: e.target.value,
                            }))
                          }
                          onBlur={() => {
                            const to = deliverValue(b).trim()
                            if (b.isDefault && to)
                              onSetDefaultBinding(b.index, to)
                          }}
                        />
                      )}
                      {noDefault && (
                        <span className="text-muted-foreground">
                          — {t("agents.channelsNoDefault")}
                        </span>
                      )}
                      {b.isDefault && (
                        <span className="text-muted-foreground">
                          — {t("agents.channelsDefault")}
                        </span>
                      )}
                    </div>
                  )
                })}
              </div>
            )}
            <p className="text-muted-foreground text-xs">
              {t("agents.channelsHint")}
            </p>
          </div>
        )}
      </div>
    </div>
  )
}
