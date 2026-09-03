import { useTranslation } from "react-i18next"

import { type CoreConfigForm } from "@/components/config/form-model"
import {
  ConfigSectionCard,
  type UpdateCoreField,
} from "@/components/config/sections/section-card"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Input } from "@/components/ui/input"

interface ContextManagementSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
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
          onChange={(e) => onFieldChange("compressMinPercent", e.target.value)}
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
        label={t("pages.config.compress_trigger_days")}
        hint={t("pages.config.compress_trigger_days_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.compressTriggerDays}
          onChange={(e) => onFieldChange("compressTriggerDays", e.target.value)}
        />
      </Field>

      <Field
        label={t("pages.config.compress_retain_max_age_days")}
        hint={t("pages.config.compress_retain_max_age_days_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.compressRetainMaxAgeDays}
          onChange={(e) =>
            onFieldChange("compressRetainMaxAgeDays", e.target.value)
          }
        />
      </Field>

      <Field
        label={t("pages.config.compress_retain_max_tokens")}
        hint={t("pages.config.compress_retain_max_tokens_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={0}
          value={form.compressRetainMaxTokens}
          onChange={(e) =>
            onFieldChange("compressRetainMaxTokens", e.target.value)
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
          onChange={(e) => onFieldChange("archiveMessageCount", e.target.value)}
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
          onChange={(e) => onFieldChange("summaryMaxCount", e.target.value)}
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
          onChange={(e) => onFieldChange("evictionBudgetBytes", e.target.value)}
        />
      </Field>
    </ConfigSectionCard>
  )
}
