import { useTranslation } from "react-i18next"

import { type ModelInfo } from "@/api/models"
import { type ProviderInfo } from "@/api/providers"
import { CLI_DEFAULT, CUSTOM_MODEL } from "@/components/setup/wizard-model"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

interface ModelStepProps {
  modelChoice: string
  setModelChoice: (v: string) => void
  isCliProvider: boolean
  selectedProvider: ProviderInfo | undefined
  presetModels: ModelInfo[]
  recommendedModelId: string | undefined
  customModel: string
  setCustomModel: (v: string) => void
  customLabel: string
  setCustomLabel: (v: string) => void
}

export function ModelStep({
  modelChoice,
  setModelChoice,
  isCliProvider,
  selectedProvider,
  presetModels,
  recommendedModelId,
  customModel,
  setCustomModel,
  customLabel,
  setCustomLabel,
}: ModelStepProps) {
  const { t } = useTranslation()

  return (
    <div className="space-y-5">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">{t("setup.model.title")}</h1>
        <p className="text-muted-foreground text-sm">{t("setup.model.body")}</p>
      </div>

      <div className="space-y-2">
        <Label>{t("setup.model.selectLabel")}</Label>
        <Select value={modelChoice} onValueChange={setModelChoice}>
          <SelectTrigger>
            <SelectValue placeholder={t("setup.model.selectPlaceholder")} />
          </SelectTrigger>
          <SelectContent>
            {isCliProvider && (
              <SelectItem value={CLI_DEFAULT}>
                {t("setup.model.cliDefaultOption")}
              </SelectItem>
            )}
            {presetModels
              // For CLIs, hide the sentinel "no-model" presets — the
              // Default option above covers them.
              .filter(
                (m) => !isCliProvider || m.model !== selectedProvider?.protocol,
              )
              .map((m) => (
                <SelectItem key={m.index} value={m.model_name}>
                  {m.model_name} ({m.model})
                  {m.model === recommendedModelId
                    ? ` — ${t("setup.model.recommended")}`
                    : ""}
                </SelectItem>
              ))}
            <SelectItem value={CUSTOM_MODEL}>
              {t("setup.model.customOption")}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>

      {modelChoice === CUSTOM_MODEL && (
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="space-y-2">
            <Label>{t("setup.model.customIdLabel")}</Label>
            <Input
              value={customModel}
              placeholder="gpt-4o"
              onChange={(e) => setCustomModel(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label>{t("setup.model.customNameLabel")}</Label>
            <Input
              value={customLabel}
              placeholder={t("setup.model.customNamePlaceholder")}
              onChange={(e) => setCustomLabel(e.target.value)}
            />
          </div>
        </div>
      )}
    </div>
  )
}
