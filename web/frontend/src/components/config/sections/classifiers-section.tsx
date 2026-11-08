import { useTranslation } from "react-i18next"

import {
  CLASSIFIER_DEFAULTS,
  type CoreConfigForm,
} from "@/components/config/form-model"
import {
  ConfigSectionCard,
  type UpdateCoreField,
} from "@/components/config/sections/section-card"
import { Field } from "@/components/shared-form"
import { Input } from "@/components/ui/input"

interface ClassifiersSectionProps {
  form: CoreConfigForm
  onFieldChange: UpdateCoreField
}

// The install's classifier settings (classifiers.processed_days and
// classifiers.limits). A blank field uses the default, shown as the
// placeholder.
const CLASSIFIER_FIELDS = [
  {
    key: "classifierProcessedDays",
    label: "classifiers_processed_days",
    def: "processed_days",
  },
  {
    key: "classifierMaxRules",
    label: "classifiers_max_rules",
    def: "max_rules",
  },
  {
    key: "classifierMaxPromptChars",
    label: "classifiers_max_prompt_chars",
    def: "max_prompt_chars",
  },
  {
    key: "classifierMaxItemChars",
    label: "classifiers_max_item_chars",
    def: "max_item_chars",
  },
  {
    key: "classifierMaxItemsPerRun",
    label: "classifiers_max_items_per_run",
    def: "max_items_per_run",
  },
  {
    key: "classifierMaxParallel",
    label: "classifiers_max_parallel",
    def: "max_parallel",
  },
  {
    key: "classifierMaxActivityHours",
    label: "classifiers_max_activity_hours",
    def: "max_activity_hours",
  },
  {
    key: "classifierMinEverySeconds",
    label: "classifiers_min_every_seconds",
    def: "min_every_seconds",
  },
] as const

// classifierDefault is the value a field has when it is left blank.
function classifierDefault(
  def: (typeof CLASSIFIER_FIELDS)[number]["def"],
): number {
  return def === "processed_days"
    ? CLASSIFIER_DEFAULTS.processed_days
    : CLASSIFIER_DEFAULTS.limits[def]
}

export function ClassifiersSection({
  form,
  onFieldChange,
}: ClassifiersSectionProps) {
  const { t } = useTranslation()

  return (
    <ConfigSectionCard
      title={t("pages.config.sections.classifiers")}
      description={t("pages.config.classifiers_desc")}
    >
      {CLASSIFIER_FIELDS.map(({ key, label, def }) => (
        <Field
          key={key}
          label={t(`pages.config.${label}`)}
          hint={t("pages.config.classifiers_default", {
            value: classifierDefault(def),
          })}
          layout="setting-row"
        >
          <Input
            data-testid={`classifiers-${def}`}
            type="number"
            min={0}
            placeholder={String(classifierDefault(def))}
            value={form[key]}
            onChange={(e) => onFieldChange(key, e.target.value)}
          />
        </Field>
      ))}
    </ConfigSectionCard>
  )
}
