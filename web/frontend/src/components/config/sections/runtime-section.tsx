import { useTranslation } from "react-i18next"

import { type CoreConfigForm } from "@/components/config/form-model"
import {
  ConfigSectionCard,
  type UpdateCoreField,
} from "@/components/config/sections/section-card"
import { Field } from "@/components/shared-form"
import { Input } from "@/components/ui/input"

interface RuntimeSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
}

export function RuntimeSection({ form, onFieldChange }: RuntimeSectionProps) {
  const { t } = useTranslation()

  return (
    <ConfigSectionCard title={t("pages.config.sections.runtime")}>
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
