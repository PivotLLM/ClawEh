import { IconRefresh } from "@tabler/icons-react"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import { LogsPanel } from "@/components/logs/logs-panel"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { type LogSource, useGatewayLogs } from "@/hooks/use-gateway-logs"

const LINE_OPTIONS = [100, 250, 500, 1000, 2000]

export function LogsPage() {
  const { t } = useTranslation()
  const [lines, setLines] = useState(250)
  const [source, setSource] = useState<LogSource>("log")
  const { logs, error, loading, refresh } = useGatewayLogs(lines, source)

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.logs")}>
        <div className="flex items-center gap-2">
          <Select
            value={source}
            onValueChange={(v) => setSource(v as LogSource)}
          >
            <SelectTrigger className="w-36" aria-label={t("pages.logs.source")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="log">{t("pages.logs.source_log")}</SelectItem>
              <SelectItem value="alerts">
                {t("pages.logs.source_alerts")}
              </SelectItem>
            </SelectContent>
          </Select>
          <span className="text-muted-foreground text-sm">
            {t("pages.logs.lines")}
          </span>
          <Select
            value={String(lines)}
            onValueChange={(v) => setLines(Number(v))}
          >
            <SelectTrigger className="w-28">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {LINE_OPTIONS.map((n) => (
                <SelectItem key={n} value={String(n)}>
                  {n}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void refresh()}
            disabled={loading}
          >
            <IconRefresh
              className={`size-4 ${loading ? "animate-spin" : ""}`}
            />
            {t("pages.logs.refresh")}
          </Button>
        </div>
      </PageHeader>

      <div className="flex flex-1 flex-col gap-4 overflow-hidden p-4 sm:p-8">
        {error && (
          <div className="text-destructive bg-destructive/10 rounded-lg px-4 py-3 text-sm">
            {error}
          </div>
        )}
        <LogsPanel
          logs={logs}
          emptyText={
            source === "alerts" ? t("pages.logs.empty_alerts") : undefined
          }
        />
      </div>
    </div>
  )
}
