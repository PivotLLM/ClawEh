import type { ReactNode } from "react"
import { useTranslation } from "react-i18next"

import {
  type CoreConfigForm,
  SESSION_MODE_OPTIONS,
  type LauncherForm,
} from "@/components/config/form-model"
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

type UpdateLauncherField = <K extends keyof LauncherForm>(
  key: K,
  value: LauncherForm[K],
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
        label={t("pages.config.workspace")}
        hint={t("pages.config.workspace_hint")}
        layout="setting-row"
      >
        <Input
          value={form.workspace}
          onChange={(e) => onFieldChange("workspace", e.target.value)}
          placeholder="~/.claw/workspace"
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

    </ConfigSectionCard>
  )
}

interface ContextManagementSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
}

interface SummarizationModelsFieldProps {
  value: string[]
  onChange: (next: string[]) => void
}

// SummarizationModelsField edits the ordered global summarization model list.
// Models are tried in order during context compaction; the agent's own model is
// always appended as a final fallback at runtime, so an empty list is valid.
function SummarizationModelsField({
  value,
  onChange,
}: SummarizationModelsFieldProps) {
  const { t } = useTranslation()
  const { apiKeyModels, oauthModels, localModels } = useChatModels()
  const suggestions = [...apiKeyModels, ...oauthModels, ...localModels].map(
    (m) => m.model_name,
  )

  const updateAt = (index: number, next: string) => {
    const copy = [...value]
    copy[index] = next
    onChange(copy)
  }
  const removeAt = (index: number) => {
    onChange(value.filter((_, i) => i !== index))
  }
  const addRow = () => onChange([...value, ""])

  return (
    <Field
      label={t("pages.config.summarization_models")}
      hint={t("pages.config.summarization_models_hint")}
    >
      <datalist id="summarization-model-suggestions">
        {suggestions.map((name) => (
          <option key={name} value={name} />
        ))}
      </datalist>
      <div className="flex flex-col gap-2">
        {value.length === 0 && (
          <p className="text-muted-foreground text-sm">
            {t("pages.config.summarization_models_empty")}
          </p>
        )}
        {value.map((model, index) => (
          <div key={index} className="flex items-center gap-2">
            <span className="text-muted-foreground w-5 text-right text-sm tabular-nums">
              {index + 1}.
            </span>
            <Input
              className="flex-1"
              list="summarization-model-suggestions"
              value={model}
              placeholder="e.g. claude-haiku-4-5"
              onChange={(e) => updateAt(index, e.target.value)}
            />
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => removeAt(index)}
            >
              {t("pages.config.summarization_models_remove")}
            </Button>
          </div>
        ))}
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={addRow}
          >
            {t("pages.config.summarization_models_add")}
          </Button>
        </div>
      </div>
    </Field>
  )
}

interface SummarizationSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
}

// SummarizationSection is a global (not per-agent) configuration card for the
// ordered summarization model chain used during context compaction.
export function SummarizationSection({
  form,
  onFieldChange,
}: SummarizationSectionProps) {
  const { t } = useTranslation()

  return (
    <ConfigSectionCard
      title={t("pages.config.sections.summarization")}
      description={t("pages.config.sections.summarization_desc")}
    >
      <SummarizationModelsField
        value={form.summarizationModels}
        onChange={(next) => onFieldChange("summarizationModels", next)}
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

    </ConfigSectionCard>
  )
}

interface LauncherSectionProps {
  launcherForm: LauncherForm
  onFieldChange: UpdateLauncherField
  disabled: boolean
}

export function LauncherSection({
  launcherForm,
  onFieldChange,
  disabled,
}: LauncherSectionProps) {
  const { t } = useTranslation()

  return (
    <ConfigSectionCard title={t("pages.config.sections.launcher")}>
      <SwitchCardField
        label={t("pages.config.lan_access")}
        hint={t("pages.config.lan_access_hint")}
        layout="setting-row"
        checked={launcherForm.publicAccess}
        disabled={disabled}
        onCheckedChange={(checked) => onFieldChange("publicAccess", checked)}
      />

      <Field
        label={t("pages.config.server_port")}
        hint={t("pages.config.server_port_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={1}
          max={65535}
          value={launcherForm.port}
          disabled={disabled}
          onChange={(e) => onFieldChange("port", e.target.value)}
        />
      </Field>

      <Field
        label={t("pages.config.allowed_cidrs")}
        hint={t("pages.config.allowed_cidrs_hint")}
        layout="setting-row"
        controlClassName="md:max-w-md"
      >
        <Textarea
          value={launcherForm.allowedCIDRsText}
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
  autoStartEnabled: boolean
  autoStartHint: string
  autoStartDisabled: boolean
  onAutoStartChange: (checked: boolean) => void
}

export function DevicesSection({
  form,
  onFieldChange,
  autoStartEnabled,
  autoStartHint,
  autoStartDisabled,
  onAutoStartChange,
}: DevicesSectionProps) {
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

      <SwitchCardField
        label={t("pages.config.autostart_label")}
        hint={autoStartHint}
        layout="setting-row"
        checked={autoStartEnabled}
        disabled={autoStartDisabled}
        onCheckedChange={onAutoStartChange}
      />
    </ConfigSectionCard>
  )
}
