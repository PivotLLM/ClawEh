import { IconLoader2 } from "@tabler/icons-react"
import { useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import { type ModelInfo, setDefaultModel, updateModel } from "@/api/models"
import { type ProviderInfo, getProviders } from "@/api/providers"
import {
  AdvancedSection,
  Field,
  SwitchCardField,
} from "@/components/shared-form"
import {
  REASONING_EFFORT_OPTIONS,
  formatDropParams,
  formatExtraBody,
  parseDropParams,
  parseExtraBody,
} from "@/components/models/model-config-fields"
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
import { Textarea } from "@/components/ui/textarea"

interface EditForm {
  provider: string
  connectMode: string
  workspace: string
  rpm: string
  maxTokens: string
  maxTokensField: string
  requestTimeout: string
  thinkingLevel: string
  reasoningEffort: string
  extraBody: string
  dropParams: string
  noTools: boolean
}

interface EditModelSheetProps {
  model: ModelInfo | null
  open: boolean
  onClose: () => void
  onSaved: () => void
}

export function EditModelSheet({
  model,
  open,
  onClose,
  onSaved,
}: EditModelSheetProps) {
  const { t } = useTranslation()
  const [form, setForm] = useState<EditForm>({
    provider: "",
    connectMode: "",
    workspace: "",
    rpm: "",
    maxTokens: "",
    maxTokensField: "",
    requestTimeout: "",
    thinkingLevel: "",
    reasoningEffort: "",
    extraBody: "",
    dropParams: "",
    noTools: false,
  })
  const [providers, setProviders] = useState<ProviderInfo[]>([])
  const [saving, setSaving] = useState(false)
  const [setAsDefault, setSetAsDefault] = useState(false)
  const [error, setError] = useState("")

  useEffect(() => {
    if (model) {
      setForm({
        provider: model.provider ?? "",
        connectMode: model.connect_mode ?? "",
        workspace: model.workspace ?? "",
        rpm: model.rpm ? String(model.rpm) : "",
        maxTokens: model.max_tokens ? String(model.max_tokens) : "",
        maxTokensField: model.max_tokens_field ?? "",
        requestTimeout: model.request_timeout
          ? String(model.request_timeout)
          : "",
        thinkingLevel: model.thinking_level ?? "",
        reasoningEffort: model.reasoning_effort ?? "",
        extraBody: formatExtraBody(model.extra_body),
        dropParams: formatDropParams(model.drop_params),
        noTools: model.no_tools ?? false,
      })
      setSetAsDefault(model.is_default)
      setError("")
      getProviders()
        .then((data) =>
          setProviders(
            [...data.providers].sort((a, b) => a.name.localeCompare(b.name)),
          ),
        )
        .catch(() => setProviders([]))
    }
  }, [model])

  const setField =
    (key: keyof EditForm) => (e: React.ChangeEvent<HTMLInputElement>) =>
      setForm((f) => ({ ...f, [key]: e.target.value }))

  const extraBodyParsed = useMemo(
    () => parseExtraBody(form.extraBody, t),
    [form.extraBody, t],
  )

  const handleSave = async () => {
    if (!model) return
    if (extraBodyParsed.error) return
    setSaving(true)
    setError("")
    try {
      await updateModel(model.index, {
        model_name: model.model_name,
        model: model.model,
        provider: form.provider || undefined,
        connect_mode: form.connectMode || undefined,
        workspace: form.workspace || undefined,
        rpm: form.rpm ? Number(form.rpm) : undefined,
        max_tokens: form.maxTokens ? Number(form.maxTokens) : undefined,
        max_tokens_field: form.maxTokensField || undefined,
        request_timeout: form.requestTimeout
          ? Number(form.requestTimeout)
          : undefined,
        thinking_level: form.thinkingLevel || undefined,
        // Always send reasoning_effort and extra_body, even when empty:
        // handleUpdateModel merge-unmarshals into the existing entry, so an
        // absent field preserves the old value. "" / null tell the backend to
        // clear, which then drops the field via omitempty on save.
        reasoning_effort: form.reasoningEffort,
        extra_body: extraBodyParsed.value ?? null,
        // Always send drop_params: [] clears a previously-stored list, since
        // handleUpdateModel merge-unmarshals and an absent field would preserve
        // the old value (omitempty then drops the empty slice on save).
        drop_params: parseDropParams(form.dropParams),
        no_tools: form.noTools,
      })
      if (setAsDefault && !model.is_default) {
        await setDefaultModel(model.model_name)
      }
      onSaved()
      onClose()
    } catch (e) {
      setError(e instanceof Error ? e.message : t("models.edit.saveError"))
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
            {t("models.edit.title", { name: model?.model_name })}
          </SheetTitle>
          <SheetDescription className="font-mono text-xs">
            {model?.model}
          </SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="space-y-5 px-6 py-5">
            <Field
              label={t("models.field.provider")}
              hint={t("models.field.providerHint")}
            >
              <Select
                value={form.provider || undefined}
                onValueChange={(v) => setForm((f) => ({ ...f, provider: v }))}
              >
                <SelectTrigger className="w-full">
                  <SelectValue
                    placeholder={t("models.field.providerPlaceholder")}
                  />
                </SelectTrigger>
                <SelectContent>
                  {providers.map((p) => (
                    <SelectItem key={p.index} value={p.name}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            <SwitchCardField
              label={t("models.defaultOnSave.label")}
              hint={t("models.defaultOnSave.description")}
              checked={setAsDefault}
              onCheckedChange={setSetAsDefault}
            />

            <AdvancedSection>
              <Field
                label={t("models.field.connectMode")}
                hint={t("models.field.connectModeHint")}
              >
                <Input
                  value={form.connectMode}
                  onChange={setField("connectMode")}
                  placeholder="stdio"
                />
              </Field>

              <Field
                label={t("models.field.workspace")}
                hint={t("models.field.workspaceHint")}
              >
                <Input
                  value={form.workspace}
                  onChange={setField("workspace")}
                  placeholder="/path/to/workspace"
                />
              </Field>

              <Field
                label={t("models.field.requestTimeout")}
                hint={t("models.field.requestTimeoutHint")}
              >
                <Input
                  value={form.requestTimeout}
                  onChange={setField("requestTimeout")}
                  placeholder="60"
                  type="number"
                  min={0}
                />
              </Field>

              <Field
                label={t("models.field.rpm")}
                hint={t("models.field.rpmHint")}
              >
                <Input
                  value={form.rpm}
                  onChange={setField("rpm")}
                  placeholder="60"
                  type="number"
                  min={0}
                />
              </Field>

              <Field
                label={t("models.field.thinkingLevel")}
                hint={t("models.field.thinkingLevelHint")}
              >
                <Input
                  value={form.thinkingLevel}
                  onChange={setField("thinkingLevel")}
                  placeholder="off"
                />
              </Field>

              <Field
                label={t("models.field.reasoningEffort")}
                hint={t("models.field.reasoningEffortHint")}
              >
                <Select
                  value={form.reasoningEffort === "" ? "__unset__" : form.reasoningEffort}
                  onValueChange={(v) =>
                    setForm((f) => ({
                      ...f,
                      reasoningEffort: v === "__unset__" ? "" : v,
                    }))
                  }
                >
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__unset__">
                      {t("models.field.reasoningEffortUnset")}
                    </SelectItem>
                    {REASONING_EFFORT_OPTIONS.map((opt) => (
                      <SelectItem key={opt} value={opt}>
                        {opt}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>

              <Field
                label={t("models.field.extraBody")}
                hint={t("models.field.extraBodyHint")}
                error={extraBodyParsed.error}
              >
                <Textarea
                  value={form.extraBody}
                  onChange={(e) =>
                    setForm((f) => ({ ...f, extraBody: e.target.value }))
                  }
                  placeholder={t("models.field.extraBodyPlaceholder")}
                  className="font-mono text-xs"
                  rows={6}
                  aria-invalid={!!extraBodyParsed.error}
                />
              </Field>

              <Field
                label={t("models.field.dropParams")}
                hint={t("models.field.dropParamsHint")}
              >
                <Input
                  value={form.dropParams}
                  onChange={setField("dropParams")}
                  placeholder="temperature, top_p"
                />
              </Field>

              <SwitchCardField
                label="Tools Enabled"
                hint="When off, no tools are passed to this model. Disable for models that don't support tool calling."
                checked={!form.noTools}
                onCheckedChange={(v) => setForm((f) => ({ ...f, noTools: !v }))}
              />

              <Field
                label={t("models.field.maxTokens")}
                hint={t("models.field.maxTokensHint")}
              >
                <Input
                  value={form.maxTokens}
                  onChange={setField("maxTokens")}
                  placeholder="8192"
                  type="number"
                  min={0}
                />
              </Field>

              <Field
                label={t("models.field.maxTokensField")}
                hint={t("models.field.maxTokensFieldHint")}
              >
                <Input
                  value={form.maxTokensField}
                  onChange={setField("maxTokensField")}
                  placeholder="max_completion_tokens"
                />
              </Field>
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
          <Button
            onClick={handleSave}
            disabled={saving || !!extraBodyParsed.error}
          >
            {saving && <IconLoader2 className="size-4 animate-spin" />}
            {t("common.save")}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
