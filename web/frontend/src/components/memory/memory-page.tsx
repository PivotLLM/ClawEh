import { IconBrain, IconChevronRight } from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type MemoryDomain,
  type MemoryMemory,
  getMemoryStore,
  getMemoryStores,
} from "@/api/memory"
import { PageHeader } from "@/components/page-header"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"

function Pill({ children }: { children: React.ReactNode }) {
  return (
    <span className="bg-muted text-muted-foreground rounded px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide">
      {children}
    </span>
  )
}

function MemoryRow({ m }: { m: MemoryMemory }) {
  return (
    <div className="border-border/40 border-b py-2 last:border-0">
      <div className="flex items-start gap-2">
        <Pill>{m.type}</Pill>
        <span className="flex-1 text-sm">{m.text}</span>
      </div>
      <div className="text-muted-foreground mt-1 flex gap-3 text-[11px]">
        <span>conf {m.confidence.toFixed(2)}</span>
        <span>prio {m.priority}</span>
        {m.origin && <span>from {m.origin}</span>}
        <span>{m.source}</span>
        <span>{new Date(m.updated).toLocaleString()}</span>
      </div>
    </div>
  )
}

function DomainCard({ d }: { d: MemoryDomain }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(true)
  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className="border-border rounded-lg border"
    >
      <CollapsibleTrigger className="flex w-full items-center gap-2 px-3 py-2 text-left">
        <IconChevronRight
          className={`size-4 transition-transform ${open ? "rotate-90" : ""}`}
        />
        <span className="font-medium">{d.name}</span>
        {d.sticky && <Pill>{t("pages.memory.sticky")}</Pill>}
        <span className="text-muted-foreground ml-auto text-xs">
          {t("pages.memory.memory_count", { count: d.memories.length })}
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent className="px-3 pb-2">
        {d.summary && (
          <p className="text-muted-foreground mb-2 text-xs italic">{d.summary}</p>
        )}
        {d.triggers && (
          <p className="text-muted-foreground mb-2 text-[11px]">
            {t("pages.memory.triggers")}: {d.triggers}
          </p>
        )}
        {d.memories.length === 0 ? (
          <p className="text-muted-foreground text-xs">
            {t("pages.memory.no_memories")}
          </p>
        ) : (
          d.memories.map((m) => <MemoryRow key={m.id} m={m} />)
        )}
      </CollapsibleContent>
    </Collapsible>
  )
}

export function MemoryPage() {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<string | null>(null)

  const { data: stores, isLoading: storesLoading } = useQuery({
    queryKey: ["memory-stores"],
    queryFn: getMemoryStores,
  })

  useEffect(() => {
    if (!selected && stores && stores.length > 0) {
      setSelected(stores[0].id)
    }
  }, [stores, selected])

  const { data: detail, isLoading: detailLoading } = useQuery({
    queryKey: ["memory-store", selected],
    queryFn: () => getMemoryStore(selected as string),
    enabled: selected !== null,
  })

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.memory")} />
      <div className="flex flex-1 overflow-hidden">
        {/* Store list */}
        <div className="border-border w-72 shrink-0 overflow-auto border-r p-3">
          {storesLoading ? (
            <div className="text-muted-foreground text-sm">
              {t("labels.loading")}
            </div>
          ) : !stores || stores.length === 0 ? (
            <div className="text-muted-foreground text-sm">
              {t("pages.memory.empty")}
            </div>
          ) : (
            <div className="space-y-1">
              {stores.map((s) => (
                <button
                  key={s.id}
                  onClick={() => setSelected(s.id)}
                  className={`flex w-full flex-col items-start rounded-md px-2 py-1.5 text-left text-sm ${
                    selected === s.id
                      ? "bg-muted"
                      : "hover:bg-muted/50"
                  }`}
                >
                  <span className="flex w-full items-center gap-1.5">
                    <IconBrain className="size-3.5 shrink-0" />
                    <span className="truncate font-medium">{s.agent}</span>
                  </span>
                  <span className="text-muted-foreground truncate text-[11px]">
                    {new Date(s.updated).toLocaleString()}
                  </span>
                </button>
              ))}
            </div>
          )}
        </div>

        {/* Detail */}
        <div className="flex-1 overflow-auto p-4">
          {selected === null ? (
            <div className="text-muted-foreground text-sm">
              {t("pages.memory.select_prompt")}
            </div>
          ) : detailLoading ? (
            <div className="text-muted-foreground text-sm">
              {t("labels.loading")}
            </div>
          ) : !detail ? (
            <div className="text-destructive text-sm">
              {t("pages.memory.load_error")}
            </div>
          ) : (
            <div className="mx-auto max-w-[900px] space-y-4">
              <div className="text-muted-foreground flex flex-wrap gap-4 text-sm">
                <span>
                  {t("pages.memory.active_domains")}: {detail.active_domains}
                </span>
                <span>
                  {t("pages.memory.active_memories")}: {detail.active_memories}
                </span>
                <span>
                  {t("pages.memory.pending")}: {detail.pending}
                </span>
                {detail.last_run ? (
                  <span>
                    {t("pages.memory.last_run")}:{" "}
                    {new Date(detail.last_run.started_at).toLocaleString()} —{" "}
                    {detail.last_run.trigger}/{detail.last_run.status} (
                    {detail.last_run.ops_applied})
                  </span>
                ) : (
                  <span>{t("pages.memory.last_run")}: {t("pages.memory.never")}</span>
                )}
              </div>

              {detail.last_run?.error && (
                <div className="text-destructive text-xs">
                  {detail.last_run.error}
                </div>
              )}

              {detail.domains.map((d) => (
                <DomainCard key={d.id} d={d} />
              ))}

              {detail.pending_list.length > 0 && (
                <div className="border-border rounded-lg border">
                  <div className="border-border border-b px-3 py-2 font-medium">
                    {t("pages.memory.pending_review")} (
                    {detail.pending_list.length})
                  </div>
                  <div className="px-3 pb-2">
                    {detail.pending_list.map((m) => (
                      <MemoryRow key={m.id} m={m} />
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
