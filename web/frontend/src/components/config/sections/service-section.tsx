import { useTranslation } from "react-i18next"

import { type CoreConfigForm } from "@/components/config/form-model"
import {
  ConfigSectionCard,
  type UpdateCoreField,
} from "@/components/config/sections/section-card"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"

interface ServiceSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
  // Optional: disables the inputs while a save is in flight. Unused under
  // auto-save (fields stay live); defaults to false.
  disabled?: boolean
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
  disabled = false,
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
