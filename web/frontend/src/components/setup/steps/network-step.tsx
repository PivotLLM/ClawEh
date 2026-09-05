import { useTranslation } from "react-i18next"

import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

interface NetworkStepProps {
  networkAccess: boolean
  setNetworkAccess: (v: boolean) => void
  gatewayPort: string
  setGatewayPort: (v: string) => void
  externalUrl: string
  setExternalUrl: (v: string) => void
  externalUrlDefault: string
}

export function NetworkStep({
  networkAccess,
  setNetworkAccess,
  gatewayPort,
  setGatewayPort,
  externalUrl,
  setExternalUrl,
  externalUrlDefault,
}: NetworkStepProps) {
  const { t } = useTranslation()

  return (
    <div className="space-y-5">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">{t("setup.network.title")}</h1>
        <p className="text-muted-foreground text-sm">
          {t("setup.network.body")}
        </p>
      </div>

      <div className="border-border/60 bg-card flex items-start justify-between gap-3 rounded-xl border p-4">
        <div className="min-w-0 space-y-1">
          <Label>{t("setup.network.accessLabel")}</Label>
          <p className="text-muted-foreground text-xs leading-normal">
            {t("setup.network.accessHint")}
          </p>
        </div>
        <Switch
          checked={networkAccess}
          onCheckedChange={setNetworkAccess}
          aria-label={t("setup.network.accessLabel")}
        />
      </div>

      <div className="space-y-2">
        <Label>{t("setup.network.portLabel")}</Label>
        <Input
          type="number"
          min={1}
          max={65535}
          value={gatewayPort}
          onChange={(e) => setGatewayPort(e.target.value)}
        />
        <p className="text-muted-foreground text-xs">
          {t("setup.network.portHint")}
        </p>
      </div>

      <div className="space-y-2">
        <Label>{t("setup.network.externalLabel")}</Label>
        <Input
          type="text"
          value={externalUrl}
          placeholder={externalUrlDefault}
          onChange={(e) => setExternalUrl(e.target.value)}
        />
        <p className="text-muted-foreground text-xs">
          {t("setup.network.externalHint")}
        </p>
      </div>

      <p className="text-muted-foreground text-xs">
        {t("setup.network.restartNote")}
      </p>
    </div>
  )
}
