import { useTranslation } from "react-i18next"

import {
  type CoreConfigForm,
  FORUM_LIMIT_DEFAULTS,
} from "@/components/config/form-model"
import {
  ConfigSectionCard,
  type UpdateCoreField,
} from "@/components/config/sections/section-card"
import { Field } from "@/components/shared-form"
import { Input } from "@/components/ui/input"

interface ForumSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
}

// The install's maximums for every forum (forum.limits). A blank field uses
// the default, shown as the placeholder.
const FIELDS = [
  { key: "forumMaxCalls", label: "forum_max_calls", def: "max_calls" },
  {
    key: "forumMaxDurationSeconds",
    label: "forum_max_duration_seconds",
    def: "max_duration_seconds",
  },
  {
    key: "forumCallTimeoutSeconds",
    label: "forum_call_timeout_seconds",
    def: "call_timeout_seconds",
  },
  {
    key: "forumMaxParallelCalls",
    label: "forum_max_parallel_calls",
    def: "max_parallel_calls",
  },
] as const

export function ForumSection({ form, onFieldChange }: ForumSectionProps) {
  const { t } = useTranslation()

  return (
    <ConfigSectionCard
      title={t("pages.config.sections.forum")}
      description={t("pages.config.forum_desc")}
    >
      {FIELDS.map(({ key, label, def }) => (
        <Field
          key={key}
          label={t(`pages.config.${label}`)}
          hint={t("pages.config.forum_default", {
            value: FORUM_LIMIT_DEFAULTS[def],
          })}
          layout="setting-row"
        >
          <Input
            data-testid={`forum-${def}`}
            type="number"
            min={1}
            placeholder={String(FORUM_LIMIT_DEFAULTS[def])}
            value={form[key]}
            onChange={(e) => onFieldChange(key, e.target.value)}
          />
        </Field>
      ))}
    </ConfigSectionCard>
  )
}
