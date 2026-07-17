import { useEffect, useRef } from "react"
import { useTranslation } from "react-i18next"

import { AnsiLogLine } from "@/components/logs/ansi-log-line"
import { ScrollArea } from "@/components/ui/scroll-area"

type LogsPanelProps = {
  logs: string[]
}

export function LogsPanel({ logs }: LogsPanelProps) {
  const { t } = useTranslation()
  const scrollRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollIntoView({ behavior: "smooth" })
    }
  }, [logs])

  return (
    <div className="relative flex-1 overflow-hidden rounded-lg border border-zinc-800 bg-zinc-950 text-zinc-100">
      <ScrollArea className="h-full">
        <div className="relative p-4 font-mono text-sm leading-relaxed">
          {logs.length === 0 ? (
            <div className="text-zinc-500 italic">{t("pages.logs.empty")}</div>
          ) : (
            logs.map((log, index) => <AnsiLogLine key={index} line={log} />)
          )}
          <div ref={scrollRef} />
        </div>
      </ScrollArea>
    </div>
  )
}
