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
import { useGatewayLogs } from "@/hooks/use-gateway-logs"

const LINE_OPTIONS = [100, 250, 500, 1000, 2000]

export function LogsPage() {
  const { t } = useTranslation()
  const [lines, setLines] = useState(250)
  const { logs, error, loading, refresh } = useGatewayLogs(lines)

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title={t("navigation.logs")}
        children={
          <div className="flex items-center gap-2">
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
        }
      />

      <div className="flex flex-1 flex-col gap-4 overflow-hidden p-4 sm:p-8">
        {error && (
          <div className="text-destructive bg-destructive/10 rounded-lg px-4 py-3 text-sm">
            {error}
          </div>
        )}
        <LogsPanel logs={logs} />
      </div>
    </div>
  )
}
