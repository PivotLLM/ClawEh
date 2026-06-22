import { IconLoader2 } from "@tabler/icons-react"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { type ProviderInfo, updateProvider } from "@/api/providers"
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

interface EditForm {
  name: string
  protocol: string
  baseURL: string
  apiKey: string
  proxy: string
  command: string
  strictCompat: boolean
  noParallelToolCalls: boolean
  responseFormatJSON: boolean
}

interface EditProviderSheetProps {
  provider: ProviderInfo | null
  open: boolean
  onClose: () => void
  onSaved: () => void
}

export function EditProviderSheet({
  provider,
  open,
  onClose,
  onSaved,
}: EditProviderSheetProps) {
  const { t } = useTranslation()
  const [form, setForm] = useState<EditForm>({
    name: "",
    protocol: "openai",
    baseURL: "",
    apiKey: "",
    proxy: "",
    command: "",
    strictCompat: false,
    noParallelToolCalls: false,
    responseFormatJSON: false,
  })
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const cli = isCliProtocol(form.protocol)
  const configured = Boolean(provider?.api_key)

  useEffect(() => {
    if (provider) {
      setForm({
        name: provider.name,
        protocol: provider.protocol,
        baseURL: provider.base_url ?? "",
        apiKey: "",
        proxy: provider.proxy ?? "",
        command: provider.command ?? "",
        strictCompat: provider.strict_compat ?? false,
        noParallelToolCalls: provider.no_parallel_tool_calls ?? false,
        responseFormatJSON: provider.response_format_json ?? false,
      })
      setError("")
    }
  }, [provider])

  const setField =
    (key: keyof EditForm) => (e: React.ChangeEvent<HTMLInputElement>) =>
      setForm((f) => ({ ...f, [key]: e.target.value }))

  const handleSave = async () => {
    if (!provider) return
    setSaving(true)
    setError("")
    try {
      const cliProto = isCliProtocol(form.protocol)
      await updateProvider(provider.index, {
        name: form.name.trim(),
        protocol: form.protocol,
        base_url: cliProto ? undefined : form.baseURL.trim() || undefined,
        // Empty api_key keeps the stored key (backend semantics).
        api_key: cliProto ? undefined : form.apiKey.trim() || undefined,
        proxy: form.proxy.trim() || undefined,
        command: cliProto ? form.command.trim() || undefined : undefined,
        strict_compat: form.strictCompat,
        no_parallel_tool_calls: form.noParallelToolCalls,
        response_format_json: form.responseFormatJSON,
      })
      onSaved()
      onClose()
    } catch (e) {
      setError(e instanceof Error ? e.message : t("providers.edit.saveError"))
    } finally {
      setSaving(false)
    }
  }

  const apiKeyPlaceholder = configured
    ? maskedSecretPlaceholder(
        provider?.api_key ?? "",
        t("providers.field.apiKeyPlaceholderSet"),
      )
    : t("providers.field.apiKeyPlaceholder")

  return (
    <Sheet open={open} onOpenChange={(v) => !v && onClose()}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:!w-full data-[side=right]:sm:!w-[560px] data-[side=right]:sm:!max-w-[560px]"
      >
        <SheetHeader className="border-b-muted border-b px-6 py-5">
          <SheetTitle className="text-base">
            {t("providers.edit.title", { name: provider?.name })}
          </SheetTitle>
          <SheetDescription className="font-mono text-xs">
            {provider?.protocol}
          </SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="space-y-5 px-6 py-5">
            <Field
              label={t("providers.field.name")}
              hint={t("providers.field.nameHint")}
            >
              <Input value={form.name} onChange={setField("name")} />
            </Field>

            <Field
              label={t("providers.field.protocol")}
              hint={t("providers.field.protocolHint")}
            >
              <Select
                value={form.protocol}
                onValueChange={(v) => setForm((f) => ({ ...f, protocol: v }))}
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
                />
              </Field>
            ) : (
              <>
                <Field
                  label={t("providers.field.baseURL")}
                  hint={
                    requiresBaseURL(form.protocol)
                      ? t("providers.field.baseURLRequiredHint")
                      : undefined
                  }
                >
                  <Input
                    value={form.baseURL}
                    onChange={setField("baseURL")}
                    placeholder="https://api.example.com/v1"
                  />
                </Field>

                <Field
                  label={t("providers.field.apiKey")}
                  hint={configured ? t("providers.edit.apiKeyHint") : undefined}
                >
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

            {error && (
              <p className="text-destructive bg-destructive/10 rounded-md px-3 py-2 text-sm">
                {error}
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
            {t("common.save")}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
