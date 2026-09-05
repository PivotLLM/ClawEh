import { useTranslation } from "react-i18next"

import { type CoreConfigForm } from "@/components/config/form-model"
import {
  ConfigSectionCard,
  type UpdateCoreField,
} from "@/components/config/sections/section-card"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Input } from "@/components/ui/input"

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
