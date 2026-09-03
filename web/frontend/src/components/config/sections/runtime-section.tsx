import { useTranslation } from "react-i18next"

import {
  type CoreConfigForm,
  SESSION_MODE_OPTIONS,
} from "@/components/config/form-model"
import {
  ConfigSectionCard,
  type UpdateCoreField,
} from "@/components/config/sections/section-card"
import { Field } from "@/components/shared-form"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

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
