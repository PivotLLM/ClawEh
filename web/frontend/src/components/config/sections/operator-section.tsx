import { useTranslation } from "react-i18next"

import { type CoreConfigForm } from "@/components/config/form-model"
import {
  ConfigSectionCard,
  type UpdateCoreField,
} from "@/components/config/sections/section-card"
import { Field } from "@/components/shared-form"
import { Input } from "@/components/ui/input"

interface OperatorSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
}

// The operator's own settings (operator.*): the name their own messages carry
// when a device has no User of its own.
export function OperatorSection({ form, onFieldChange }: OperatorSectionProps) {
  const { t } = useTranslation()

  return (
    <ConfigSectionCard title={t("pages.config.sections.operator")}>
      <Field
        label={t("pages.config.operator_name")}
        hint={t("pages.config.operator_name_hint")}
        layout="setting-row"
      >
        <Input
          id="operator-name"
          data-testid="operator-name"
          value={form.operatorName}
          onChange={(e) => onFieldChange("operatorName", e.target.value)}
        />
      </Field>
    </ConfigSectionCard>
  )
}
