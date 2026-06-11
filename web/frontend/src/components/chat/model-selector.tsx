import { useTranslation } from "react-i18next"

import type { ModelInfo } from "@/api/models"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

interface ModelSelectorProps {
  defaultModelName: string
  models: ModelInfo[]
  onValueChange: (modelName: string) => void
}

export function ModelSelector({
  defaultModelName,
  models,
  onValueChange,
}: ModelSelectorProps) {
  const { t } = useTranslation()

  return (
    <Select value={defaultModelName} onValueChange={onValueChange}>
      <SelectTrigger
        size="sm"
        className="text-muted-foreground hover:text-foreground focus-visible:border-input h-8 max-w-[160px] min-w-[80px] bg-transparent shadow-none focus-visible:ring-0 sm:max-w-[220px]"
      >
        <SelectValue placeholder={t("chat.noModel")} />
      </SelectTrigger>
      <SelectContent position="popper" align="start">
        {models.map((model) => (
          <SelectItem key={model.index} value={model.model_name}>
            {model.model_name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
