import { useTranslation } from "react-i18next"

import { type CoreConfigForm } from "@/components/config/form-model"
import {
  ConfigSectionCard,
  type UpdateCoreField,
} from "@/components/config/sections/section-card"
import { SwitchCardField } from "@/components/shared-form"

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
