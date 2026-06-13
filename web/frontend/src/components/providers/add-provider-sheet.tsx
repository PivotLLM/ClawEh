import { IconLoader2 } from "@tabler/icons-react"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { addProvider } from "@/api/providers"
import {
  PROTOCOL_OPTIONS,
  isCliProtocol,
  requiresBaseURL,
} from "@/components/providers/provider-config-fields"
import { maskedSecretPlaceholder } from "@/components/secret-placeholder"
import {
  AdvancedSection,
  Field,
  KeyInput,
  SwitchCardField,
} from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"

interface AddForm {
  name: string
  protocol: string
  baseURL: string
  apiKey: string
  proxy: string
  authMethod: string
  command: string
  strictCompat: boolean
  noParallelToolCalls: boolean
  responseFormatJSON: boolean
}

const EMPTY_ADD_FORM: AddForm = {
  name: "",
  protocol: "openai",
  baseURL: "",
  apiKey: "",
  proxy: "",
  authMethod: "",
  command: "",
  strictCompat: false,
  noParallelToolCalls: false,
  responseFormatJSON: false,
}

interface AddProviderSheetProps {
  open: boolean
  onClose: () => void
  onSaved: () => void
  existingNames: string[]
}

export function AddProviderSheet({
  open,
  onClose,
  onSaved,
  existingNames,
}: AddProviderSheetProps) {
  const { t } = useTranslation()
  const [form, setForm] = useState<AddForm>(EMPTY_ADD_FORM)
  const [saving, setSaving] = useState(false)
  const [fieldErrors, setFieldErrors] = useState<
    Partial<Record<keyof AddForm, string>>
  >({})
  const [serverError, setServerError] = useState("")
  const apiKeyPlaceholder = maskedSecretPlaceholder(
    form.apiKey,
    t("providers.field.apiKeyPlaceholder"),
  )
  const cli = isCliProtocol(form.protocol)

  useEffect(() => {
    if (open) {
      setForm(EMPTY_ADD_FORM)
      setFieldErrors({})
      setServerError("")
    }
  }, [open])

  const validate = (): boolean => {
    const errors: Partial<Record<keyof AddForm, string>> = {}
    const name = form.name.trim()
    if (!name) {
      errors.name = t("providers.add.errorRequired")
    } else if (existingNames.some((n) => n.trim() === name)) {
      errors.name = t("providers.add.errorDuplicateName")
    }
    if (isCliProtocol(form.protocol)) {
      if (!form.command.trim()) errors.command = t("providers.add.errorRequired")
    } else if (requiresBaseURL(form.protocol) && !form.baseURL.trim()) {
      errors.baseURL = t("providers.add.errorRequired")
    }
    setFieldErrors(errors)
    return Object.keys(errors).length === 0
  }

  const setField =
    (key: keyof AddForm) => (e: React.ChangeEvent<HTMLInputElement>) => {
      setForm((f) => ({ ...f, [key]: e.target.value }))
      if (fieldErrors[key]) {
        setFieldErrors((prev) => ({ ...prev, [key]: undefined }))
      }
    }

  const handleSave = async () => {
    if (!validate()) return
    setSaving(true)
    setServerError("")
    try {
      const cliProto = isCliProtocol(form.protocol)
      await addProvider({
        name: form.name.trim(),
        protocol: form.protocol,
        base_url: cliProto ? undefined : form.baseURL.trim() || undefined,
        api_key: cliProto ? undefined : form.apiKey.trim() || undefined,
        proxy: form.proxy.trim() || undefined,
        auth_method: form.authMethod.trim() || undefined,
        command: cliProto ? form.command.trim() || undefined : undefined,
        strict_compat: form.strictCompat,
        no_parallel_tool_calls: form.noParallelToolCalls,
        response_format_json: form.responseFormatJSON,
      })
      onSaved()
      onClose()
    } catch (e) {
      setServerError(
        e instanceof Error ? e.message : t("providers.add.saveError"),
      )
    } finally {
      setSaving(false)
    }
  }

  return (
    <Sheet open={open} onOpenChange={(v) => !v && onClose()}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:!w-full data-[side=right]:sm:!w-[560px] data-[side=right]:sm:!max-w-[560px]"
      >
        <SheetHeader className="border-b-muted border-b px-6 py-5">
          <SheetTitle className="text-base">
            {t("providers.add.title")}
          </SheetTitle>
          <SheetDescription className="text-xs">
            {t("providers.add.description")}
          </SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="space-y-5 px-6 py-5">
            <Field
              label={t("providers.field.name")}
              hint={t("providers.field.nameHint")}
            >
              <Input
                value={form.name}
                onChange={setField("name")}
                placeholder={t("providers.field.namePlaceholder")}
                aria-invalid={!!fieldErrors.name}
              />
              {fieldErrors.name && (
                <p className="text-destructive text-xs">{fieldErrors.name}</p>
              )}
            </Field>

            <Field
              label={t("providers.field.protocol")}
              hint={t("providers.field.protocolHint")}
            >
              <Select
                value={form.protocol}
                onValueChange={(v) =>
                  setForm((f) => ({ ...f, protocol: v }))
                }
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {[...PROTOCOL_OPTIONS].sort((a, b) => a.localeCompare(b)).map((opt) => (
                    <SelectItem key={opt} value={opt}>
                      {opt}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            {cli ? (
              <Field
                label={t("providers.field.command")}
                hint={t("providers.field.commandHint")}
              >
                <Input
                  value={form.command}
                  onChange={setField("command")}
                  placeholder="/usr/local/bin/claude"
                  className="font-mono text-sm"
                  aria-invalid={!!fieldErrors.command}
                />
                {fieldErrors.command && (
                  <p className="text-destructive text-xs">
                    {fieldErrors.command}
                  </p>
                )}
              </Field>
            ) : (
              <>
                <Field label={t("providers.field.baseURL")}>
                  <Input
                    value={form.baseURL}
                    onChange={setField("baseURL")}
                    placeholder="https://api.example.com/v1"
                    aria-invalid={!!fieldErrors.baseURL}
                  />
                  {fieldErrors.baseURL && (
                    <p className="text-destructive text-xs">
                      {fieldErrors.baseURL}
                    </p>
                  )}
                </Field>

                <Field label={t("providers.field.apiKey")}>
                  <KeyInput
                    value={form.apiKey}
                    onChange={(v) => setForm((f) => ({ ...f, apiKey: v }))}
                    placeholder={apiKeyPlaceholder}
                  />
                </Field>
              </>
            )}

            <AdvancedSection>
              <Field
                label={t("providers.field.proxy")}
                hint={t("providers.field.proxyHint")}
              >
                <Input
                  value={form.proxy}
                  onChange={setField("proxy")}
                  placeholder="http://127.0.0.1:7890"
                />
              </Field>

              <Field
                label={t("providers.field.authMethod")}
                hint={t("providers.field.authMethodHint")}
              >
                <Input
                  value={form.authMethod}
                  onChange={setField("authMethod")}
                  placeholder="oauth"
                />
              </Field>

              <SwitchCardField
                label={t("providers.field.strictCompat")}
                hint={t("providers.field.strictCompatHint")}
                checked={form.strictCompat}
                onCheckedChange={(v) =>
                  setForm((f) => ({ ...f, strictCompat: v }))
                }
              />

              <SwitchCardField
                label={t("providers.field.noParallelToolCalls")}
                hint={t("providers.field.noParallelToolCallsHint")}
                checked={form.noParallelToolCalls}
                onCheckedChange={(v) =>
                  setForm((f) => ({ ...f, noParallelToolCalls: v }))
                }
              />

              <SwitchCardField
                label={t("providers.field.responseFormatJSON")}
                hint={t("providers.field.responseFormatJSONHint")}
                checked={form.responseFormatJSON}
                onCheckedChange={(v) =>
                  setForm((f) => ({ ...f, responseFormatJSON: v }))
                }
              />
            </AdvancedSection>

            {serverError && (
              <p className="text-destructive bg-destructive/10 rounded-md px-3 py-2 text-sm">
                {serverError}
              </p>
            )}
          </div>
        </div>

        <SheetFooter className="border-t-muted border-t px-6 py-4">
          <Button variant="ghost" onClick={onClose} disabled={saving}>
            {t("common.cancel")}
          </Button>
          <Button onClick={handleSave} disabled={saving}>
            {saving && <IconLoader2 className="size-4 animate-spin" />}
            {t("providers.add.confirm")}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
