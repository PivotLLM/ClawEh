import { IconLoader2 } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import { CLI_DEFAULT, CUSTOM_MODEL } from "@/components/setup/wizard-model"

interface ReviewStepProps {
  providerName: string
  modelChoice: string
  customLabel: string
  customModel: string
  agentName: string
  finishError: string
  finishing: boolean
}

export function ReviewStep({
  providerName,
  modelChoice,
  customLabel,
  customModel,
  agentName,
  finishError,
  finishing,
}: ReviewStepProps) {
  const { t } = useTranslation()

  return (
    <div className="space-y-5">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">{t("setup.review.title")}</h1>
        <p className="text-muted-foreground text-sm">
          {t("setup.review.body")}
        </p>
      </div>
      <dl className="border-border/60 divide-border/60 divide-y rounded-xl border text-sm">
        <div className="flex justify-between p-3">
          <dt className="text-muted-foreground">{t("setup.steps.provider")}</dt>
          <dd className="font-medium">{providerName}</dd>
        </div>
        <div className="flex justify-between p-3">
          <dt className="text-muted-foreground">{t("setup.steps.model")}</dt>
          <dd className="font-medium">
            {modelChoice === CLI_DEFAULT
              ? t("setup.model.cliDefaultOption")
              : modelChoice === CUSTOM_MODEL
                ? customLabel.trim() || customModel.trim()
                : modelChoice}
          </dd>
        </div>
        <div className="flex justify-between p-3">
          <dt className="text-muted-foreground">{t("setup.steps.agent")}</dt>
          <dd className="font-medium">{agentName.trim()}</dd>
        </div>
      </dl>
      {finishError && <p className="text-destructive text-sm">{finishError}</p>}
      {finishing && (
        <p className="text-muted-foreground flex items-center gap-2 text-sm">
          <IconLoader2 className="size-4 animate-spin" />
          {t("setup.applying")}
        </p>
      )}
    </div>
  )
}
