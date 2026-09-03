import { IconCheck, IconLoader2, IconX } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import { type ProviderInfo } from "@/api/providers"
import { type CLIInfo } from "@/api/system"
import { type TestState } from "@/components/setup/wizard-model"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

interface ProviderStepProps {
  providerName: string
  onProviderChange: (v: string) => void
  apiProviders: ProviderInfo[]
  cliProviders: ProviderInfo[]
  cliFor: (protocol: string) => CLIInfo | undefined
  clis: CLIInfo[]
  selectedProvider: ProviderInfo | undefined
  isCliProvider: boolean
  selectedCli: CLIInfo | undefined
  apiKey: string
  onApiKeyChange: (v: string) => void
  runTest: () => void
  testState: TestState
  testMessage: string
}

export function ProviderStep({
  providerName,
  onProviderChange,
  apiProviders,
  cliProviders,
  cliFor,
  clis,
  selectedProvider,
  isCliProvider,
  selectedCli,
  apiKey,
  onApiKeyChange,
  runTest,
  testState,
  testMessage,
}: ProviderStepProps) {
  const { t } = useTranslation()

  return (
    <div className="space-y-5">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">{t("setup.provider.title")}</h1>
        <p className="text-muted-foreground text-sm">
          {t("setup.provider.body")}
        </p>
      </div>

      <div className="space-y-2">
        <Label>{t("setup.provider.selectLabel")}</Label>
        <Select value={providerName} onValueChange={onProviderChange}>
          <SelectTrigger>
            <SelectValue placeholder={t("setup.provider.selectPlaceholder")} />
          </SelectTrigger>
          <SelectContent>
            {apiProviders.map((p) => (
              <SelectItem key={p.index} value={p.name}>
                {p.name}
              </SelectItem>
            ))}
            {cliProviders.map((p) => {
              const info = cliFor(p.protocol)
              return (
                <SelectItem
                  key={p.index}
                  value={p.name}
                  disabled={!info?.installed}
                >
                  {p.name}
                  {info?.installed
                    ? ` — ${t("setup.provider.cliInstalled")}`
                    : ` — ${t("setup.provider.cliMissing")}`}
                </SelectItem>
              )
            })}
          </SelectContent>
        </Select>
        <p className="text-muted-foreground text-xs">
          {t("setup.provider.cliHint")}
        </p>
      </div>

      {clis.length > 0 && (
        <div className="border-border/60 bg-card space-y-2 rounded-xl border p-4 text-sm">
          <p className="font-medium">
            {t("setup.provider.cliDetectedHeading")}
          </p>
          {clis.map((c) => (
            <div
              key={c.protocol}
              className="flex items-center justify-between gap-3"
            >
              <span className="flex items-center gap-2">
                {c.installed ? (
                  <IconCheck className="size-4 text-emerald-600 dark:text-emerald-400" />
                ) : (
                  <IconX className="text-muted-foreground size-4" />
                )}
                {c.label}
              </span>
              <span className="text-muted-foreground truncate text-xs">
                {c.installed
                  ? c.version || c.path
                  : t("setup.provider.cliMissing")}
              </span>
            </div>
          ))}
          <p className="text-muted-foreground text-xs">
            {t("setup.provider.cliPickHint")}
          </p>
        </div>
      )}

      {selectedProvider && isCliProvider && (
        <div className="border-border/60 bg-card space-y-1 rounded-xl border p-4 text-sm">
          {selectedCli?.installed ? (
            <>
              <p className="flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
                <IconCheck className="size-4" />
                {t("setup.provider.cliDetected")}
              </p>
              {selectedCli.version && (
                <p className="text-muted-foreground">{selectedCli.version}</p>
              )}
              {selectedCli.path && (
                <p className="text-muted-foreground font-mono text-xs">
                  {selectedCli.path}
                </p>
              )}
            </>
          ) : (
            <p className="text-destructive flex items-center gap-1">
              <IconX className="size-4" />
              {t("setup.provider.cliNotDetected")}
            </p>
          )}
        </div>
      )}

      {selectedProvider && !isCliProvider && (
        <>
          {selectedProvider.base_url && (
            <p className="text-muted-foreground text-xs">
              {t("setup.provider.endpoint")}: {selectedProvider.base_url}
            </p>
          )}
          <div className="space-y-2">
            <Label>{t("setup.provider.keyLabel")}</Label>
            <Input
              type="password"
              value={apiKey}
              autoComplete="off"
              placeholder={t("setup.provider.keyPlaceholder")}
              onChange={(e) => {
                onApiKeyChange(e.target.value)
              }}
            />
          </div>

          <div className="flex items-center gap-3">
            <Button
              variant="outline"
              onClick={runTest}
              disabled={testState === "testing"}
            >
              {testState === "testing" ? (
                <IconLoader2 className="size-4 animate-spin" />
              ) : null}
              {t("setup.provider.testButton")}
            </Button>
            {testState === "ok" && (
              <span className="flex items-center gap-1 text-sm text-emerald-600 dark:text-emerald-400">
                <IconCheck className="size-4" /> {testMessage}
              </span>
            )}
            {testState === "warn" && (
              <span className="text-muted-foreground text-sm">
                {testMessage}
              </span>
            )}
            {testState === "fail" && (
              <span className="text-destructive flex items-center gap-1 text-sm">
                <IconX className="size-4" /> {testMessage}
              </span>
            )}
          </div>
        </>
      )}
    </div>
  )
}
