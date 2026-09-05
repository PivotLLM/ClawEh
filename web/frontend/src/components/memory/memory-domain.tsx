import {
  IconArchive,
  IconArrowBackUp,
  IconChevronRight,
  IconTrash,
} from "@tabler/icons-react"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import {
  MEMORY_TYPES,
  MEMORY_TYPE_HINTS,
  type MemoryDomain,
  type MemoryMemory,
  type MemoryType,
} from "@/api/memory"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

export function Pill({ children }: { children: React.ReactNode }) {
  return (
    <span className="bg-muted text-muted-foreground rounded px-1.5 py-0.5 text-[10px] font-medium tracking-wide uppercase">
      {children}
    </span>
  )
}

/** The controls one memory row offers. Passed down rather than wired here so
 *  the page owns the store id and the refresh. */
export interface MemoryRowActions {
  onRetype: (m: MemoryMemory, type: MemoryType) => void
  onSetStatus: (m: MemoryMemory, status: "active" | "retired") => void
  onDelete: (m: MemoryMemory) => void
  selected: boolean
  onSelect: (m: MemoryMemory, selected: boolean) => void
  busy: boolean
}

export function MemoryRow({
  m,
  actions,
}: {
  m: MemoryMemory
  actions: MemoryRowActions
}) {
  const { t } = useTranslation()
  const retired = m.status === "retired"
  return (
    <div
      data-testid="memory-row"
      data-memory-id={m.id}
      data-memory-type={m.type}
      data-memory-status={m.status}
      className={`border-border/40 border-b py-2 last:border-0 ${retired ? "opacity-60" : ""}`}
    >
      <div className="flex items-start gap-2">
        <Checkbox
          checked={actions.selected}
          onCheckedChange={(v) => actions.onSelect(m, v === true)}
          aria-label={t("pages.memory.select_memory")}
          className="mt-0.5 shrink-0"
        />
        <span className="flex-1 text-sm">{m.text}</span>

        {/* Type is the correction that matters most: it decides whether the
            memory is in the prompt at all, and the assistant chose it at write
            time with no way to revisit. */}
        <Select
          value={m.type}
          disabled={actions.busy}
          onValueChange={(v) => actions.onRetype(m, v as MemoryType)}
        >
          <SelectTrigger
            size="sm"
            className="h-7 w-[9.5rem] shrink-0 text-xs"
            aria-label={t("pages.memory.type_of", {
              text: m.text.slice(0, 40),
            })}
          >
            <SelectValue>{m.type}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {MEMORY_TYPES.map((ty) => (
              <SelectItem key={ty} value={ty}>
                <div className="flex flex-col gap-0.5">
                  <span className="font-medium">{ty}</span>
                  <span className="text-muted-foreground text-xs">
                    {MEMORY_TYPE_HINTS[ty]}
                  </span>
                </div>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <button
          onClick={() => actions.onSetStatus(m, retired ? "active" : "retired")}
          disabled={actions.busy}
          title={
            retired
              ? t("pages.memory.restore_hint")
              : t("pages.memory.retire_hint")
          }
          aria-label={
            retired
              ? t("pages.memory.restore_memory")
              : t("pages.memory.retire_memory")
          }
          className="text-muted-foreground hover:text-foreground shrink-0 disabled:opacity-50"
        >
          {retired ? (
            <IconArrowBackUp className="size-3.5" />
          ) : (
            <IconArchive className="size-3.5" />
          )}
        </button>
        <button
          onClick={() => actions.onDelete(m)}
          disabled={actions.busy}
          title={t("pages.memory.delete_hint")}
          aria-label={t("pages.memory.delete_memory")}
          className="text-muted-foreground hover:text-destructive shrink-0 disabled:opacity-50"
        >
          <IconTrash className="size-3.5" />
        </button>
      </div>
      <div className="text-muted-foreground mt-1 flex flex-wrap gap-3 pl-6 text-[11px]">
        <span>conf {m.confidence.toFixed(2)}</span>
        {m.origin && <span>from {m.origin}</span>}
        {retired && <Pill>{t("pages.memory.retired")}</Pill>}
        {m.type === "event" && (
          <span title={MEMORY_TYPE_HINTS.event}>
            {t("pages.memory.not_in_context")}
          </span>
        )}
        {m.file_ref && (
          <span title={t("pages.memory.file_hint")}>file {m.file_ref}</span>
        )}
        <span>{new Date(m.updated).toLocaleString()}</span>
      </div>
    </div>
  )
}

export function DomainCard({
  d,
  onDeleteDomain,
  onAddMemory,
  rowActions,
}: {
  d: MemoryDomain
  onDeleteDomain: (d: MemoryDomain) => void
  onAddMemory: (d: MemoryDomain) => void
  rowActions: (m: MemoryMemory) => MemoryRowActions
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(true)
  // Events are counted separately because they are never in the prompt: a
  // domain of 300 memories is a different thing depending on how many of them
  // the assistant actually sees.
  const events = d.memories.filter((m) => m.type === "event").length
  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className="border-border rounded-lg border"
      data-testid="memory-domain"
      data-domain-name={d.name}
    >
      <div className="flex w-full items-center gap-2 px-3 py-2">
        <CollapsibleTrigger className="flex flex-1 items-center gap-2 text-left">
          <IconChevronRight
            className={`size-4 transition-transform ${open ? "rotate-90" : ""}`}
          />
          <span className="font-medium">{d.name}</span>
          {d.sticky && <Pill>{t("pages.memory.sticky")}</Pill>}
          <span className="text-muted-foreground ml-auto text-xs">
            {t("pages.memory.memory_count", { count: d.memories.length })}
            {events > 0 &&
              ` · ${t("pages.memory.event_count", { count: events })}`}
          </span>
        </CollapsibleTrigger>
        <button
          onClick={() => onAddMemory(d)}
          className="text-muted-foreground hover:text-foreground shrink-0 text-xs"
          aria-label={t("pages.memory.add_memory_to", { name: d.name })}
        >
          {t("pages.memory.add_memory")}
        </button>
        <button
          onClick={() => onDeleteDomain(d)}
          title={t("pages.memory.delete_domain_hint")}
          aria-label={t("pages.memory.delete_domain", { name: d.name })}
          className="text-muted-foreground hover:text-destructive shrink-0"
        >
          <IconTrash className="size-4" />
        </button>
      </div>
      <CollapsibleContent className="px-3 pb-2">
        {d.summary && (
          <p className="text-muted-foreground mb-2 text-xs italic">
            {d.summary}
          </p>
        )}
        {d.triggers && (
          <p className="text-muted-foreground mb-2 text-[11px]">
            {t("pages.memory.triggers")}: {d.triggers}
          </p>
        )}
        {d.keyword_triggers && (
          <p className="text-muted-foreground mb-2 text-[11px]">
            {t("pages.memory.keyword_triggers")}: {d.keyword_triggers}
          </p>
        )}
        {d.last_used && (
          <p className="text-muted-foreground mb-2 text-[11px]">
            {t("pages.memory.last_used")}:{" "}
            {new Date(d.last_used).toLocaleDateString()}
          </p>
        )}
        {d.memories.length === 0 ? (
          <p className="text-muted-foreground text-xs">
            {t("pages.memory.no_memories")}
          </p>
        ) : (
          d.memories.map((m) => (
            <MemoryRow key={m.id} m={m} actions={rowActions(m)} />
          ))
        )}
      </CollapsibleContent>
    </Collapsible>
  )
}
