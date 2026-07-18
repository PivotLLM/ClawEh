import { useState, type ReactNode } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type CoreConfigForm,
  SESSION_MODE_OPTIONS,
} from "@/components/config/form-model"
import { FallbacksSelect } from "@/components/agents/model-selects"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { useChatModels } from "@/hooks/use-chat-models"

type UpdateCoreField = <K extends keyof CoreConfigForm>(
  key: K,
  value: CoreConfigForm[K],
) => void

interface ConfigSectionCardProps {
  title: string
  description?: string
  children: ReactNode
}

function ConfigSectionCard({
  title,
  description,
  children,
}: ConfigSectionCardProps) {
  return (
    <Card size="sm">
      <CardHeader className="border-border border-b">
        <CardTitle>{title}</CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent className="pt-0">
        <div className="divide-border/70 divide-y">{children}</div>
      </CardContent>
    </Card>
  )
}

interface AgentDefaultsSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
}

export function AgentDefaultsSection({
  form,
  onFieldChange,
}: AgentDefaultsSectionProps) {
  const { t } = useTranslation()

  return (
    <ConfigSectionCard title={t("pages.config.sections.agent")}>
      <Field
        label={t("pages.config.base_dir")}
        hint={t("pages.config.base_dir_hint")}
        layout="setting-row"
      >
        <Input
          value={form.baseDir}
          onChange={(e) => onFieldChange("baseDir", e.target.value)}
          placeholder="~/.claw/agents"
        />
      </Field>

      <Field
        label={t("pages.config.common_dir")}
        hint={t("pages.config.common_dir_hint")}
        layout="setting-row"
      >
        <Input
          value={form.commonDir}
          onChange={(e) => onFieldChange("commonDir", e.target.value)}
          placeholder="<agents>/common"
        />
      </Field>

      <SwitchCardField
        label={t("pages.config.restrict_workspace")}
        hint={t("pages.config.restrict_workspace_hint")}
        layout="setting-row"
        checked={form.restrictToWorkspace}
        onCheckedChange={(checked) =>
          onFieldChange("restrictToWorkspace", checked)
        }
      />

      <SwitchCardField
        label={t("pages.config.allow_remote")}
        hint={t("pages.config.allow_remote_hint")}
        layout="setting-row"
        checked={form.allowRemote}
        onCheckedChange={(checked) => onFieldChange("allowRemote", checked)}
      />

      <SwitchCardField
        label={t("pages.config.stream_tool_activity")}
        hint={t("pages.config.stream_tool_activity_hint")}
        layout="setting-row"
        checked={form.streamToolActivity}
        onCheckedChange={(checked) =>
          onFieldChange("streamToolActivity", checked)
        }
      />

      <Field
        label={t("pages.config.max_tokens")}
        hint={t("pages.config.max_tokens_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={1}
          value={form.maxTokens}
          onChange={(e) => onFieldChange("maxTokens", e.target.value)}
        />
      </Field>

      <Field
        label={t("pages.config.max_tool_iterations")}
        hint={t("pages.config.max_tool_iterations_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={1}
          value={form.maxToolIterations}
          onChange={(e) => onFieldChange("maxToolIterations", e.target.value)}
        />
      </Field>

      <Field
        label={t("pages.config.request_timeout")}
        hint={t("pages.config.request_timeout_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.requestTimeout}
          onChange={(e) => onFieldChange("requestTimeout", e.target.value)}
        />
      </Field>

      <Field
        label={t("pages.config.turn_timeout")}
        hint={t("pages.config.turn_timeout_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.turnTimeout}
          onChange={(e) => onFieldChange("turnTimeout", e.target.value)}
        />
      </Field>

      <Field
        label={t("pages.config.max_subagent_depth")}
        hint={t("pages.config.max_subagent_depth_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={1}
          value={form.maxSubagentDepth}
          onChange={(e) => onFieldChange("maxSubagentDepth", e.target.value)}
        />
      </Field>

    </ConfigSectionCard>
  )
}

interface ContextManagementSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
}

interface ModelChainFieldProps {
  value: string[]
  onChange: (next: string[]) => void
  label: string
  hint: string
  emptyText: string
  addText: string
  moveUpTitle: string
  removeTitle: string
}

// ModelChainField edits an ordered list of configured models (tried in order).
// A themed Select adds models and each chosen model is an ordered row with
// move-up / remove controls. Reused by the summarization and vision model
// chains — both are side-model lists selected from the configured models.
function ModelChainField({
  value,
  onChange,
  label,
  hint,
  emptyText,
  addText,
  moveUpTitle,
  removeTitle,
}: ModelChainFieldProps) {
  const { configuredModels } = useChatModels()
  const available = [...configuredModels].sort((a, b) =>
    a.model_name.localeCompare(b.model_name),
  )
  const remaining = available.filter((m) => !value.includes(m.model_name))

  const moveUp = (i: number) => {
    if (i === 0) return
    const next = [...value]
    ;[next[i - 1], next[i]] = [next[i], next[i - 1]]
    onChange(next)
  }
  const removeAt = (i: number) => {
    onChange(value.filter((_, idx) => idx !== i))
  }
  const add = (name: string) => {
    if (!name || value.includes(name)) return
    onChange([...value, name])
  }

  return (
    <Field label={label} hint={hint}>
      <div className="flex flex-col gap-1.5">
        {value.length === 0 && (
          <p className="text-muted-foreground text-sm">{emptyText}</p>
        )}
        {value.map((model, index) => (
          <div key={model} className="flex items-center gap-1.5">
            <span className="text-muted-foreground w-5 text-right text-sm tabular-nums">
              {index + 1}.
            </span>
            <span className="border-border/50 bg-muted/40 flex-1 rounded px-2 py-1 font-mono text-xs">
              {model}
            </span>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              onClick={() => moveUp(index)}
              disabled={index === 0}
              className="text-muted-foreground size-6"
              title={moveUpTitle}
            >
              ↑
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              onClick={() => removeAt(index)}
              className="text-muted-foreground hover:text-destructive size-6"
              title={removeTitle}
            >
              ×
            </Button>
          </div>
        ))}
        {remaining.length > 0 && (
          <Select value="" onValueChange={add}>
            <SelectTrigger className="h-8 text-sm">
              <SelectValue placeholder={addText} />
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
      </div>
    </Field>
  )
}

interface ModelListFieldProps {
  value: string[]
  onChange: (next: string[]) => void
}

// SummarizationModelsField: the ordered global summarization/memory model chain.
// Models are tried in order during context compaction; the agent's own model is
// always appended as a final fallback at runtime, so an empty list is valid.
function SummarizationModelsField({ value, onChange }: ModelListFieldProps) {
  const { t } = useTranslation()
  return (
    <ModelChainField
      value={value}
      onChange={onChange}
      label={t("pages.config.summarization_models")}
      hint={t("pages.config.summarization_models_hint")}
      emptyText={t("pages.config.summarization_models_empty")}
      addText={t("pages.config.summarization_models_add")}
      moveUpTitle={t("pages.config.summarization_models_move_up")}
      removeTitle={t("pages.config.summarization_models_remove")}
    />
  )
}

// VisionModelsField: the ordered global vision-describe model chain. When an
// agent's model can't see images, images are dispatched to the first working
// model here for a text description (fallbacks tried in order). Empty = off
// (images are dropped for non-vision models, as before).
function VisionModelsField({ value, onChange }: ModelListFieldProps) {
  const { t } = useTranslation()
  return (
    <ModelChainField
      value={value}
      onChange={onChange}
      label={t("pages.config.vision_models")}
      hint={t("pages.config.vision_models_hint")}
      emptyText={t("pages.config.vision_models_empty")}
      addText={t("pages.config.vision_models_add")}
      moveUpTitle={t("pages.config.vision_models_move_up")}
      removeTitle={t("pages.config.vision_models_remove")}
    />
  )
}

interface AgentModelDefaultsSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
  agentOptions: { id: string; name?: string }[]
}

// AgentModelDefaultsSection consolidates the model-related agent defaults that
// used to be split between the Agents page (default agent, default model) and
// the Config page (summarization model chain): the default agent for unrouted
// messages, the default models (tried in order) + temperature applied to agents
// with no override, and the global summarization model chain.
export function AgentModelDefaultsSection({
  form,
  onFieldChange,
  agentOptions,
}: AgentModelDefaultsSectionProps) {
  const { t } = useTranslation()
  const { configuredModels } = useChatModels()

  return (
    <ConfigSectionCard
      title={t("pages.config.sections.agent_defaults")}
      description={t("pages.config.sections.agent_defaults_desc")}
    >
      {agentOptions.length > 0 && (
        <Field
          label={t("pages.config.default_agent")}
          hint={t("pages.config.default_agent_hint")}
          layout="setting-row"
        >
          <Select
            value={form.defaultAgentId || agentOptions[0]?.id || ""}
            onValueChange={(v) => onFieldChange("defaultAgentId", v)}
          >
            <SelectTrigger className="w-56">
              <SelectValue placeholder={t("pages.config.default_agent")} />
            </SelectTrigger>
            <SelectContent>
              {agentOptions.map((a) => (
                <SelectItem key={a.id} value={a.id}>
                  {a.name || a.id}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}

      <Field
        label={t("pages.config.default_model")}
        hint={t("pages.config.default_model_models_hint", "Models tried in order; index 0 first.")}
      >
        <FallbacksSelect
          fallbacks={form.defaultModels}
          primary=""
          models={configuredModels}
          onChange={(next) => onFieldChange("defaultModels", next)}
        />
      </Field>

      <Field
        label={t("pages.config.default_temperature")}
        hint={t("pages.config.default_temperature_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          max={2}
          step={0.1}
          value={form.defaultTemperature}
          onChange={(e) => onFieldChange("defaultTemperature", e.target.value)}
          placeholder="default"
          className="w-24"
        />
      </Field>

      <SummarizationModelsField
        value={form.summarizationModels}
        onChange={(next) => onFieldChange("summarizationModels", next)}
      />

      <VisionModelsField
        value={form.visionModels}
        onChange={(next) => onFieldChange("visionModels", next)}
      />

      <SwitchCardField
        label={t("pages.config.summarization_debug_capture")}
        hint={t("pages.config.summarization_debug_capture_hint")}
        layout="setting-row"
        checked={form.summarizationDebugCapture}
        onCheckedChange={(checked) =>
          onFieldChange("summarizationDebugCapture", checked)
        }
      />
    </ConfigSectionCard>
  )
}

export function ContextManagementSection({
  form,
  onFieldChange,
}: ContextManagementSectionProps) {
  const { t } = useTranslation()

  return (
    <ConfigSectionCard
      title={t("pages.config.sections.context_management")}
      description={t("pages.config.sections.context_management_desc")}
    >
      <Field
        label={t("pages.config.compress_normal_percent")}
        hint={t("pages.config.compress_normal_percent_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          max={100}
          value={form.compressNormalPercent}
          onChange={(e) =>
            onFieldChange("compressNormalPercent", e.target.value)
          }
        />
      </Field>

      <Field
        label={t("pages.config.compress_safety_percent")}
        hint={t("pages.config.compress_safety_percent_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          max={100}
          value={form.compressSafetyPercent}
          onChange={(e) =>
            onFieldChange("compressSafetyPercent", e.target.value)
          }
        />
      </Field>

      <Field
        label={t("pages.config.compress_min_percent")}
        hint={t("pages.config.compress_min_percent_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          max={100}
          value={form.compressMinPercent}
          onChange={(e) =>
            onFieldChange("compressMinPercent", e.target.value)
          }
        />
      </Field>

      <Field
        label={t("pages.config.compress_message_threshold")}
        hint={t("pages.config.compress_message_threshold_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.compressMessageThreshold}
          onChange={(e) =>
            onFieldChange("compressMessageThreshold", e.target.value)
          }
        />
      </Field>

      <Field
        label={t("pages.config.compress_retain_token_percent")}
        hint={t("pages.config.compress_retain_token_percent_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          max={100}
          value={form.compressRetainTokenPercent}
          onChange={(e) =>
            onFieldChange("compressRetainTokenPercent", e.target.value)
          }
        />
      </Field>

      <Field
        label={t("pages.config.compress_retain_min_messages")}
        hint={t("pages.config.compress_retain_min_messages_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.compressRetainMinMessages}
          onChange={(e) =>
            onFieldChange("compressRetainMinMessages", e.target.value)
          }
        />
      </Field>

      <Field
        label={t("pages.config.archive_message_count")}
        hint={t("pages.config.archive_message_count_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.archiveMessageCount}
          onChange={(e) =>
            onFieldChange("archiveMessageCount", e.target.value)
          }
        />
      </Field>

      <Field
        label={t("pages.config.archive_days")}
        hint={t("pages.config.archive_days_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.archiveDays}
          onChange={(e) => onFieldChange("archiveDays", e.target.value)}
        />
      </Field>

      <Field
        label={t("pages.config.summary_max_count")}
        hint={t("pages.config.summary_max_count_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.summaryMaxCount}
          onChange={(e) =>
            onFieldChange("summaryMaxCount", e.target.value)
          }
        />
      </Field>

      <Field
        label={t("pages.config.summary_retention_days")}
        hint={t("pages.config.summary_retention_days_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.summaryRetentionDays}
          onChange={(e) =>
            onFieldChange("summaryRetentionDays", e.target.value)
          }
        />
      </Field>

      <SwitchCardField
        label={t("pages.config.eviction_enabled")}
        hint={t("pages.config.eviction_enabled_hint")}
        checked={form.evictionEnabled}
        onCheckedChange={(checked) => onFieldChange("evictionEnabled", checked)}
        layout="setting-row"
      />
      <SwitchCardField
        label={t("pages.config.eviction_notify_user")}
        hint={t("pages.config.eviction_notify_user_hint")}
        checked={form.evictionNotifyUser}
        onCheckedChange={(checked) =>
          onFieldChange("evictionNotifyUser", checked)
        }
        layout="setting-row"
      />
      <Field
        label={t("pages.config.eviction_protect_turns")}
        hint={t("pages.config.eviction_protect_turns_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.evictionProtectTurns}
          onChange={(e) =>
            onFieldChange("evictionProtectTurns", e.target.value)
          }
        />
      </Field>
      <Field
        label={t("pages.config.eviction_evict_turns")}
        hint={t("pages.config.eviction_evict_turns_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.evictionEvictTurns}
          onChange={(e) => onFieldChange("evictionEvictTurns", e.target.value)}
        />
      </Field>
      <Field
        label={t("pages.config.eviction_budget_bytes")}
        hint={t("pages.config.eviction_budget_bytes_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.evictionBudgetBytes}
          onChange={(e) =>
            onFieldChange("evictionBudgetBytes", e.target.value)
          }
        />
      </Field>
    </ConfigSectionCard>
  )
}

interface RuntimeSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
}

export function RuntimeSection({ form, onFieldChange }: RuntimeSectionProps) {
  const { t } = useTranslation()
  const selectedSessionModeOption = SESSION_MODE_OPTIONS.find(
    (scope) => scope.value === form.sessionMode,
  )

  return (
    <ConfigSectionCard title={t("pages.config.sections.runtime")}>
      <Field
        label={t("pages.config.session_mode")}
        hint={t("pages.config.session_mode_hint")}
        layout="setting-row"
      >
        <Select
          value={form.sessionMode}
          onValueChange={(value) => onFieldChange("sessionMode", value)}
        >
          <SelectTrigger className="w-full">
            <SelectValue>
              {selectedSessionModeOption
                ? t(
                    selectedSessionModeOption.labelKey,
                    selectedSessionModeOption.labelDefault,
                  )
                : form.sessionMode}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            {SESSION_MODE_OPTIONS.map((scope) => (
              <SelectItem key={scope.value} value={scope.value}>
                <div className="flex flex-col gap-0.5">
                  <span className="font-medium">{t(scope.labelKey)}</span>
                  <span className="text-muted-foreground text-xs">
                    {t(scope.descKey)}
                  </span>
                </div>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>

      <Field
        label={t("pages.config.log_retention_days")}
        hint={t("pages.config.log_retention_days_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.logRetentionDays}
          onChange={(e) => onFieldChange("logRetentionDays", e.target.value)}
        />
      </Field>
    </ConfigSectionCard>
  )
}

interface BackupSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
}

export function BackupSection({ form, onFieldChange }: BackupSectionProps) {
  const { t } = useTranslation()
  const [running, setRunning] = useState(false)

  const runNow = async () => {
    setRunning(true)
    try {
      const res = await fetch("/api/backup", { method: "POST" })
      if (!res.ok) throw new Error(await res.text())
      const data = (await res.json()) as { folder?: string; files?: number }
      toast.success(t("pages.config.backup_now_done", { folder: data.folder ?? "" }))
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("pages.config.backup_now_failed"))
    } finally {
      setRunning(false)
    }
  }

  return (
    <ConfigSectionCard
      title={t("pages.config.sections.backup")}
      description={t("pages.config.backup_desc")}
    >
      <SwitchCardField
        label={t("pages.config.backup_enabled")}
        hint={t("pages.config.backup_enabled_hint")}
        checked={form.backupEnabled}
        onCheckedChange={(checked) => onFieldChange("backupEnabled", checked)}
        layout="setting-row"
      />
      <Field
        label={t("pages.config.backup_at")}
        hint={t("pages.config.backup_at_hint")}
        layout="setting-row"
      >
        <Input
          type="time"
          value={form.backupAt}
          onChange={(e) => onFieldChange("backupAt", e.target.value)}
        />
      </Field>
      <Field
        label={t("pages.config.backup_retain_days")}
        hint={t("pages.config.backup_retain_days_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={1}
          value={form.backupRetainDays}
          onChange={(e) => onFieldChange("backupRetainDays", e.target.value)}
        />
      </Field>
      <Field
        label={t("pages.config.backup_now")}
        hint={t("pages.config.backup_now_hint")}
        layout="setting-row"
      >
        <Button variant="outline" onClick={runNow} disabled={running}>
          {running ? t("pages.config.backup_now_running") : t("pages.config.backup_now")}
        </Button>
      </Field>
    </ConfigSectionCard>
  )
}

interface ServiceSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
  disabled: boolean
  // Address the user is currently reaching the WebUI on
  // (`${protocol}//${host}`), used as the external-URL placeholder/default.
  externalUrlPlaceholder: string
}

// ServiceSection owns the gateway listener settings (bind host/port + advertised
// external URL) that decide how ClawEh is reached. The bind host doubles as the
// network-access toggle: "0.0.0.0" listens on all interfaces, "127.0.0.1" stays
// loopback-only. It also keeps the network allowlist (gateway.allowed_cidrs).
export function ServiceSection({
  form,
  onFieldChange,
  disabled,
  externalUrlPlaceholder,
}: ServiceSectionProps) {
  const { t } = useTranslation()

  return (
    <ConfigSectionCard title={t("pages.config.sections.service")}>
      <SwitchCardField
        label={t("pages.config.network_access")}
        hint={t("pages.config.network_access_hint")}
        layout="setting-row"
        checked={form.gatewayHost === "0.0.0.0"}
        disabled={disabled}
        onCheckedChange={(checked) =>
          onFieldChange("gatewayHost", checked ? "0.0.0.0" : "127.0.0.1")
        }
      />

      <Field
        label={t("pages.config.web_port")}
        hint={t("pages.config.web_port_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={1}
          max={65535}
          value={form.gatewayPort}
          disabled={disabled}
          onChange={(e) => onFieldChange("gatewayPort", e.target.value)}
        />
      </Field>

      <Field
        label={t("pages.config.external_url")}
        hint={t("pages.config.external_url_hint")}
        layout="setting-row"
      >
        <Input
          type="text"
          value={form.gatewayExternalUrl}
          disabled={disabled}
          placeholder={externalUrlPlaceholder}
          onChange={(e) => onFieldChange("gatewayExternalUrl", e.target.value)}
        />
      </Field>

      <div className="py-3">
        <p className="text-muted-foreground text-xs leading-normal">
          {t("pages.config.network_restart_note")}
        </p>
      </div>

      <Field
        label={t("pages.config.allowed_cidrs")}
        hint={t("pages.config.allowed_cidrs_hint")}
        layout="setting-row"
        controlClassName="md:max-w-md"
      >
        <Textarea
          value={form.allowedCIDRsText}
          disabled={disabled}
          placeholder={t("pages.config.allowed_cidrs_placeholder")}
          className="min-h-[88px]"
          onChange={(e) => onFieldChange("allowedCIDRsText", e.target.value)}
        />
      </Field>
    </ConfigSectionCard>
  )
}

interface DevicesSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
}

export function DevicesSection({ form, onFieldChange }: DevicesSectionProps) {
  const { t } = useTranslation()

  return (
    <ConfigSectionCard title={t("pages.config.sections.devices")}>
      <SwitchCardField
        label={t("pages.config.devices_enabled")}
        hint={t("pages.config.devices_enabled_hint")}
        layout="setting-row"
        checked={form.devicesEnabled}
        onCheckedChange={(checked) => onFieldChange("devicesEnabled", checked)}
      />

      <SwitchCardField
        label={t("pages.config.monitor_usb")}
        hint={t("pages.config.monitor_usb_hint")}
        layout="setting-row"
        checked={form.monitorUSB}
        onCheckedChange={(checked) => onFieldChange("monitorUSB", checked)}
      />
    </ConfigSectionCard>
  )
}
