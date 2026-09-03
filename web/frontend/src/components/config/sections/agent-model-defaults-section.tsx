import { useTranslation } from "react-i18next"

import { FallbacksSelect } from "@/components/agents/model-selects"
import { type CoreConfigForm } from "@/components/config/form-model"
import {
  SummarizationModelsField,
  VisionModelsField,
} from "@/components/config/sections/model-fields"
import {
  ConfigSectionCard,
  type UpdateCoreField,
} from "@/components/config/sections/section-card"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useChatModels } from "@/hooks/use-chat-models"

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
        hint={t(
          "pages.config.default_model_models_hint",
          "Models tried in order; index 0 first.",
        )}
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
