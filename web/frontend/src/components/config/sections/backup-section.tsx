import { useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { type CoreConfigForm } from "@/components/config/form-model"
import {
  ConfigSectionCard,
  type UpdateCoreField,
} from "@/components/config/sections/section-card"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

interface BackupSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
}

export function BackupSection({ form, onFieldChange }: BackupSectionProps) {
  const { t } = useTranslation()
  const [running, setRunning] = useState(false)

  const runNow = async () => {
    setRunning(true)
    try {
      const res = await fetch("/api/backup", { method: "POST" })
      if (!res.ok) throw new Error(await res.text())
      const data = (await res.json()) as { folder?: string; files?: number }
      toast.success(
        t("pages.config.backup_now_done", { folder: data.folder ?? "" }),
      )
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t("pages.config.backup_now_failed"),
      )
    } finally {
      setRunning(false)
    }
  }

  return (
    <ConfigSectionCard
      title={t("pages.config.sections.backup")}
      description={t("pages.config.backup_desc")}
    >
      <SwitchCardField
        label={t("pages.config.backup_enabled")}
        hint={t("pages.config.backup_enabled_hint")}
        checked={form.backupEnabled}
        onCheckedChange={(checked) => onFieldChange("backupEnabled", checked)}
        layout="setting-row"
      />
      <Field
        label={t("pages.config.backup_at")}
        hint={t("pages.config.backup_at_hint")}
        layout="setting-row"
      >
        <Input
          type="time"
          value={form.backupAt}
          onChange={(e) => onFieldChange("backupAt", e.target.value)}
        />
      </Field>
      <Field
        label={t("pages.config.backup_retain_days")}
        hint={t("pages.config.backup_retain_days_hint")}
        layout="setting-row"
      >
        <Input
          type="number"
          min={1}
          value={form.backupRetainDays}
          onChange={(e) => onFieldChange("backupRetainDays", e.target.value)}
        />
      </Field>
      <Field
        label={t("pages.config.backup_now")}
        hint={t("pages.config.backup_now_hint")}
        layout="setting-row"
      >
        <Button variant="outline" onClick={runNow} disabled={running}>
          {running
            ? t("pages.config.backup_now_running")
            : t("pages.config.backup_now")}
        </Button>
      </Field>
    </ConfigSectionCard>
  )
}
