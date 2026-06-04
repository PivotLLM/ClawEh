import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import type { ChannelConfig } from "@/api/channels"
import { maskedSecretPlaceholder } from "@/components/secret-placeholder"
import { Field, KeyInput, SwitchCardField } from "@/components/shared-form"
import { Input } from "@/components/ui/input"

interface TelegramFormProps {
  config: ChannelConfig
  onChange: (key: string, value: unknown) => void
  isEdit: boolean
  fieldErrors?: Record<string, string>
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function asStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === "string")
}

function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>
  }
  return {}
}

function asBool(value: unknown): boolean {
  return value === true
}

function asNumberString(value: unknown): string {
  return typeof value === "number" && Number.isFinite(value) ? String(value) : ""
}

export function TelegramForm({
  config,
  onChange,
  isEdit,
  fieldErrors = {},
}: TelegramFormProps) {
  const { t } = useTranslation()
  const [allowFromDraft, setAllowFromDraft] = useState(() =>
    asStringArray(config.allow_from).join(", "),
  )
  useEffect(() => {
    setAllowFromDraft(asStringArray(config.allow_from).join(", "))
  }, [config.allow_from])

  const typingConfig = asRecord(config.typing)
  const placeholderConfig = asRecord(config.placeholder)
  const placeholderEnabled = asBool(placeholderConfig.enabled)
  const coalesceConfig = asRecord(config.coalesce)
  // Coalescing defaults to on: only an explicit `false` disables it (nil/absent → on).
  const coalesceEnabled = coalesceConfig.enabled !== false
  const tokenExtraHint =
    isEdit && asString(config.token)
      ? ` ${t("channels.field.secretHintSet")}`
      : ""

  return (
    <div className="space-y-5">
      <Field
        label={t("channels.field.token")}
        required
        hint={`${t("channels.form.desc.token")}${tokenExtraHint}`}
        error={fieldErrors.token}
      >
        <KeyInput
          value={asString(config._token)}
          onChange={(v) => onChange("_token", v)}
          placeholder={maskedSecretPlaceholder(
            config.token,
            t("channels.field.tokenPlaceholder"),
          )}
        />
      </Field>

      <Field
        label={t("channels.field.baseUrl")}
        hint={t("channels.form.desc.baseUrl")}
      >
        <Input
          value={asString(config.base_url)}
          onChange={(e) => onChange("base_url", e.target.value)}
          placeholder="https://api.telegram.org"
        />
      </Field>
      <Field
        label={t("channels.field.proxy")}
        hint={t("channels.form.desc.proxy")}
      >
        <Input
          value={asString(config.proxy)}
          onChange={(e) => onChange("proxy", e.target.value)}
          placeholder="http://127.0.0.1:7890"
        />
      </Field>
      <Field
        label={t("channels.field.allowFrom")}
        hint={t("channels.form.desc.allowFrom")}
      >
        <Input
          value={allowFromDraft}
          onChange={(e) => setAllowFromDraft(e.target.value)}
          onBlur={() =>
            onChange(
              "allow_from",
              allowFromDraft
                .split(",")
                .map((s: string) => s.trim())
                .filter(Boolean),
            )
          }
          placeholder={t("channels.field.allowFromPlaceholder")}
        />
      </Field>

      <SwitchCardField
        label={t("channels.field.typingEnabled")}
        hint={t("channels.form.desc.typingEnabled")}
        checked={asBool(typingConfig.enabled)}
        onCheckedChange={(checked) =>
          onChange("typing", { ...typingConfig, enabled: checked })
        }
        ariaLabel={t("channels.field.typingEnabled")}
      />

      <SwitchCardField
        label={t("channels.field.placeholderEnabled")}
        hint={t("channels.form.desc.placeholderEnabled")}
        checked={placeholderEnabled}
        onCheckedChange={(checked) =>
          onChange("placeholder", {
            ...placeholderConfig,
            enabled: checked,
          })
        }
        ariaLabel={t("channels.field.placeholderEnabled")}
      >
        {placeholderEnabled && (
          <div className="space-y-1">
            <Input
              value={asString(placeholderConfig.text)}
              onChange={(e) =>
                onChange("placeholder", {
                  ...placeholderConfig,
                  text: e.target.value,
                })
              }
              placeholder={t("channels.field.placeholderText")}
              aria-label={t("channels.field.placeholderText")}
            />
          </div>
        )}
      </SwitchCardField>

      <SwitchCardField
        label={t("channels.field.coalesceEnabled")}
        hint={t("channels.form.desc.coalesceEnabled")}
        checked={coalesceEnabled}
        onCheckedChange={(checked) =>
          onChange("coalesce", { ...coalesceConfig, enabled: checked })
        }
        ariaLabel={t("channels.field.coalesceEnabled")}
      >
        {coalesceEnabled && (
          <div className="space-y-1">
            <Input
              type="number"
              min={0}
              value={asNumberString(coalesceConfig.window_ms)}
              onChange={(e) => {
                const raw = e.target.value.trim()
                const parsed = raw === "" ? 0 : Number(raw)
                onChange("coalesce", {
                  ...coalesceConfig,
                  window_ms: Number.isFinite(parsed) ? parsed : 0,
                })
              }}
              placeholder="1000"
              aria-label={t("channels.field.coalesceWindowMs")}
            />
          </div>
        )}
      </SwitchCardField>
    </div>
  )
}
