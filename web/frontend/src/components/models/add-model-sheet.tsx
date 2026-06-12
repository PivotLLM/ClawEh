import { IconLoader2 } from "@tabler/icons-react"
import { useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import { addModel, setDefaultModel } from "@/api/models"
import { type ProviderInfo, getProviders } from "@/api/providers"
import {
  REASONING_EFFORT_OPTIONS,
  parseDropParams,
  parseExtraBody,
} from "@/components/models/model-config-fields"
import {
  AdvancedSection,
  Field,
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
import { Textarea } from "@/components/ui/textarea"

interface AddForm {
  modelName: string
  model: string
  provider: string
  connectMode: string
  workspace: string
  rpm: string
  maxTokensField: string
  requestTimeout: string
  thinkingLevel: string
  reasoningEffort: string
  extraBody: string
  dropParams: string
}

const EMPTY_ADD_FORM: AddForm = {
  modelName: "",
  model: "",
  provider: "",
  connectMode: "",
  workspace: "",
  rpm: "",
  maxTokensField: "",
  requestTimeout: "",
  thinkingLevel: "",
  reasoningEffort: "",
  extraBody: "",
  dropParams: "",
}

interface AddModelSheetProps {
  open: boolean
  onClose: () => void
  onSaved: () => void
  existingModelNames: string[]
}

export function AddModelSheet({
  open,
  onClose,
  onSaved,
  existingModelNames,
}: AddModelSheetProps) {
  const { t } = useTranslation()
  const [form, setForm] = useState<AddForm>(EMPTY_ADD_FORM)
  const [providers, setProviders] = useState<ProviderInfo[]>([])
  const [saving, setSaving] = useState(false)
  const [setAsDefault, setSetAsDefault] = useState(false)
  const [fieldErrors, setFieldErrors] = useState<
    Partial<Record<keyof AddForm, string>>
  >({})
  const [serverError, setServerError] = useState("")

  useEffect(() => {
    if (open) {
      setForm(EMPTY_ADD_FORM)
      setSetAsDefault(false)
      setFieldErrors({})
      setServerError("")
      getProviders()
        .then((data) =>
          setProviders(
            [...data.providers].sort((a, b) => a.name.localeCompare(b.name)),
          ),
        )
        .catch(() => setProviders([]))
    }
  }, [open])

  const validate = (): boolean => {
    const errors: Partial<Record<keyof AddForm, string>> = {}
    const modelName = form.modelName.trim()
    if (!modelName) {
      errors.modelName = t("models.add.errorRequired")
    } else if (existingModelNames.some((name) => name.trim() === modelName)) {
      errors.modelName = t("models.add.errorDuplicateModelName")
    }
    if (!form.model.trim()) errors.model = t("models.add.errorRequired")
    if (!form.provider.trim()) errors.provider = t("models.add.errorRequired")
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

  const extraBodyParsed = useMemo(
    () => parseExtraBody(form.extraBody, t),
    [form.extraBody, t],
  )

  const handleSave = async () => {
    if (!validate()) return
    if (extraBodyParsed.error) return
    setSaving(true)
    setServerError("")
    try {
      const modelName = form.modelName.trim()
      const modelId = form.model.trim()
      await addModel({
        model_name: modelName,
        model: modelId,
        provider: form.provider.trim(),
        connect_mode: form.connectMode.trim() || undefined,
        workspace: form.workspace.trim() || undefined,
        rpm: form.rpm ? Number(form.rpm) : undefined,
        max_tokens_field: form.maxTokensField.trim() || undefined,
        request_timeout: form.requestTimeout
          ? Number(form.requestTimeout)
          : undefined,
        thinking_level: form.thinkingLevel.trim() || undefined,
        reasoning_effort: form.reasoningEffort || undefined,
        extra_body: extraBodyParsed.value,
        drop_params: parseDropParams(form.dropParams),
      })
      if (setAsDefault) {
        await setDefaultModel(modelName)
      }
      onSaved()
      onClose()
    } catch (e) {
      setServerError(e instanceof Error ? e.message : t("models.add.saveError"))
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
          <SheetTitle className="text-base">{t("models.add.title")}</SheetTitle>
          <SheetDescription className="text-xs">
            {t("models.add.description")}
          </SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="space-y-5 px-6 py-5">
            <Field
              label={t("models.add.modelName")}
              hint={t("models.add.modelNameHint")}
            >
              <Input
                value={form.modelName}
                onChange={setField("modelName")}
                placeholder={t("models.add.modelNamePlaceholder")}
                aria-invalid={!!fieldErrors.modelName}
              />
              {fieldErrors.modelName && (
                <p className="text-destructive text-xs">
                  {fieldErrors.modelName}
                </p>
              )}
            </Field>

            <Field
              label={t("models.add.modelId")}
              hint={t("models.add.modelIdHint")}
            >
              <Input
                value={form.model}
                onChange={setField("model")}
                placeholder={t("models.add.modelIdPlaceholder")}
                className="font-mono text-sm"
                aria-invalid={!!fieldErrors.model}
              />
              {fieldErrors.model && (
                <p className="text-destructive text-xs">{fieldErrors.model}</p>
              )}
            </Field>

            <Field
              label={t("models.field.provider")}
              hint={t("models.field.providerHint")}
            >
              <Select
                value={form.provider || undefined}
                onValueChange={(v) => {
                  setForm((f) => ({ ...f, provider: v }))
                  if (fieldErrors.provider) {
                    setFieldErrors((prev) => ({ ...prev, provider: undefined }))
                  }
                }}
              >
                <SelectTrigger className="w-full" aria-invalid={!!fieldErrors.provider}>
                  <SelectValue placeholder={t("models.field.providerPlaceholder")} />
                </SelectTrigger>
                <SelectContent>
                  {providers.map((p) => (
                    <SelectItem key={p.index} value={p.name}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {fieldErrors.provider && (
                <p className="text-destructive text-xs">{fieldErrors.provider}</p>
              )}
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
                  value={
                    form.reasoningEffort === "" ? "__unset__" : form.reasoningEffort
                  }
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
          <Button
            onClick={handleSave}
            disabled={saving || !!extraBodyParsed.error}
          >
            {saving && <IconLoader2 className="size-4 animate-spin" />}
            {t("models.add.confirm")}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
