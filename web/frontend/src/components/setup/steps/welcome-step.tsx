import { useTranslation } from "react-i18next"

interface WelcomeStepProps {
  alreadyConfigured: boolean
}

export function WelcomeStep({ alreadyConfigured }: WelcomeStepProps) {
  const { t } = useTranslation()

  return (
    <div className="space-y-3">
      <h1 className="text-2xl font-semibold">{t("setup.welcome.title")}</h1>
      <p className="text-muted-foreground">{t("setup.welcome.body")}</p>
      <ul className="text-muted-foreground list-disc space-y-1 pl-5 text-sm">
        <li>{t("setup.welcome.point1")}</li>
        <li>{t("setup.welcome.point2")}</li>
        <li>{t("setup.welcome.point3")}</li>
      </ul>
      {alreadyConfigured && (
        <div className="rounded-xl border border-amber-500/40 bg-amber-500/10 p-4 text-sm text-amber-700 dark:text-amber-300">
          {t("setup.welcome.alreadyConfigured")}
        </div>
      )}
    </div>
  )
}
