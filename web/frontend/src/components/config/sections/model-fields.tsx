import { useMemo } from "react"
import { useTranslation } from "react-i18next"

import { Field } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useChatModels } from "@/hooks/use-chat-models"

interface ModelChainFieldProps {
  value: string[]
  onChange: (next: string[]) => void
  label: string
  hint: string
  emptyText: string
  addText: string
  moveUpTitle: string
  removeTitle: string
}

// ModelChainField edits an ordered list of configured models (tried in order).
// A themed Select adds models and each chosen model is an ordered row with
// move-up / remove controls. Reused by the summarization and vision model
// chains — both are side-model lists selected from the configured models.
function ModelChainField({
  value,
  onChange,
  label,
  hint,
  emptyText,
  addText,
  moveUpTitle,
  removeTitle,
}: ModelChainFieldProps) {
  const { configuredModels } = useChatModels()
  // Memoised: this component re-renders on every keystroke in the chain, and
  // both the copy-and-sort and the filter allocated fresh arrays each time.
  const available = useMemo(
    () =>
      [...configuredModels].sort((a, b) =>
        a.model_name.localeCompare(b.model_name),
      ),
    [configuredModels],
  )
  const remaining = useMemo(
    () => available.filter((m) => !value.includes(m.model_name)),
    [available, value],
  )

  const moveUp = (i: number) => {
    if (i === 0) return
    const next = [...value]
    ;[next[i - 1], next[i]] = [next[i], next[i - 1]]
    onChange(next)
  }
  const removeAt = (i: number) => {
    onChange(value.filter((_, idx) => idx !== i))
  }
  const add = (name: string) => {
    if (!name || value.includes(name)) return
    onChange([...value, name])
  }

  return (
    <Field label={label} hint={hint}>
      <div className="flex flex-col gap-1.5">
        {value.length === 0 && (
          <p className="text-muted-foreground text-sm">{emptyText}</p>
        )}
        {value.map((model, index) => (
          <div key={model} className="flex items-center gap-1.5">
            <span className="text-muted-foreground w-5 text-right text-sm tabular-nums">
              {index + 1}.
            </span>
            <span className="border-border/50 bg-muted/40 flex-1 rounded px-2 py-1 font-mono text-xs">
              {model}
            </span>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              onClick={() => moveUp(index)}
              disabled={index === 0}
              className="text-muted-foreground size-6"
              title={moveUpTitle}
            >
              ↑
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              onClick={() => removeAt(index)}
              className="text-muted-foreground hover:text-destructive size-6"
              title={removeTitle}
            >
              ×
            </Button>
          </div>
        ))}
        {remaining.length > 0 && (
          <Select value="" onValueChange={add}>
            <SelectTrigger className="h-8 text-sm">
              <SelectValue placeholder={addText} />
            </SelectTrigger>
            <SelectContent>
              {remaining.map((m) => (
                <SelectItem key={m.index} value={m.model_name}>
                  {m.model_name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </div>
    </Field>
  )
}

interface ModelListFieldProps {
  value: string[]
  onChange: (next: string[]) => void
}

// SummarizationModelsField: the ordered global summarization/memory model chain.
// Models are tried in order during context compaction; the agent's own model is
// always appended as a final fallback at runtime, so an empty list is valid.
export function SummarizationModelsField({
  value,
  onChange,
}: ModelListFieldProps) {
  const { t } = useTranslation()
  return (
    <ModelChainField
      value={value}
      onChange={onChange}
      label={t("pages.config.summarization_models")}
      hint={t("pages.config.summarization_models_hint")}
      emptyText={t("pages.config.summarization_models_empty")}
      addText={t("pages.config.summarization_models_add")}
      moveUpTitle={t("pages.config.summarization_models_move_up")}
      removeTitle={t("pages.config.summarization_models_remove")}
    />
  )
}

// VisionModelsField: the ordered global vision-describe model chain. When an
// agent's model can't see images, images are dispatched to the first working
// model here for a text description (fallbacks tried in order). Empty = off
// (images are dropped for non-vision models, as before).
export function VisionModelsField({ value, onChange }: ModelListFieldProps) {
  const { t } = useTranslation()
  return (
    <ModelChainField
      value={value}
      onChange={onChange}
      label={t("pages.config.vision_models")}
      hint={t("pages.config.vision_models_hint")}
      emptyText={t("pages.config.vision_models_empty")}
      addText={t("pages.config.vision_models_add")}
      moveUpTitle={t("pages.config.vision_models_move_up")}
      removeTitle={t("pages.config.vision_models_remove")}
    />
  )
}
