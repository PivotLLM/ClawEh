import { IconBrain } from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type MemoryDomain,
  type MemoryMemory,
  type MemoryType,
  bulkMemoryAction,
  createMemoryDomain,
  createMemoryItem,
  deleteMemoryDomain,
  deleteMemoryItem,
  getMemoryStore,
  getMemoryStores,
  importMemory,
  patchMemoryItem,
} from "@/api/memory"
import {
  DomainCard,
  type MemoryRowActions,
} from "@/components/memory/memory-domain"
import {
  AddDomainForm,
  AddMemoryForm,
  BulkBar,
  TransferControls,
} from "@/components/memory/memory-toolbar"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"

export function MemoryPage() {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<string | null>(null)
  const [showRetired, setShowRetired] = useState(false)
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [addingTo, setAddingTo] = useState<MemoryDomain | null>(null)
  const [addingDomain, setAddingDomain] = useState(false)
  const [busy, setBusy] = useState(false)

  const { data: stores, isLoading: storesLoading } = useQuery({
    queryKey: ["memory-stores"],
    queryFn: getMemoryStores,
  })

  // Default to the first store once the list arrives. Derived during render
  // rather than written back through an effect: an effect would render once
  // with nothing selected, then again with the default, and the detail query
  // below would be skipped on that first pass for no reason.
  const activeStore = selected ?? stores?.[0]?.id ?? null

  const { data: detail, isLoading: detailLoading } = useQuery({
    queryKey: ["memory-store", activeStore, showRetired],
    queryFn: () => getMemoryStore(activeStore as string, showRetired),
    enabled: activeStore !== null,
  })

  const qc = useQueryClient()
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["memory-store", activeStore] })
    qc.invalidateQueries({ queryKey: ["memory-stores"] })
  }

  /** Runs a mutation, reports what went wrong, and always refreshes. Every
   *  action here is a write against a store the assistant may also be writing,
   *  so the page re-reads rather than patching its own copy. */
  const act = async (what: string, fn: () => Promise<unknown>) => {
    setBusy(true)
    try {
      await fn()
      refresh()
    } catch (e) {
      window.alert(`${what}: ${e instanceof Error ? e.message : e}`)
    } finally {
      setBusy(false)
    }
  }

  const pickedIDs = () => [...picked]
  const clearSelection = () => setPicked(new Set())

  const handleSelect = (m: MemoryMemory, on: boolean) => {
    setPicked((prev) => {
      const next = new Set(prev)
      if (on) next.add(m.id)
      else next.delete(m.id)
      return next
    })
  }

  const rowActions = (m: MemoryMemory): MemoryRowActions => ({
    selected: picked.has(m.id),
    busy,
    onSelect: handleSelect,
    onRetype: (mem, type) =>
      void act(t("pages.memory.err_retype"), () =>
        patchMemoryItem(activeStore as string, mem.id, { type }),
      ),
    onSetStatus: (mem, status) =>
      void act(t("pages.memory.err_status"), () =>
        patchMemoryItem(activeStore as string, mem.id, { status }),
      ),
    onDelete: (mem) => {
      if (!window.confirm(t("pages.memory.confirm_delete_memory"))) return
      void act(t("pages.memory.err_delete"), async () => {
        await deleteMemoryItem(activeStore as string, mem.id)
        handleSelect(mem, false)
      })
    },
  })

  const handleDeleteDomain = (d: MemoryDomain) => {
    if (
      !window.confirm(
        t("pages.memory.confirm_delete_domain", {
          name: d.name,
          count: d.memories.length,
        }),
      )
    ) {
      return
    }
    void act(t("pages.memory.err_delete_domain"), () =>
      deleteMemoryDomain(activeStore as string, d.id),
    )
  }

  const bulk = (
    action: "retype" | "retire" | "restore" | "delete",
    type?: MemoryType,
  ) => {
    const ids = pickedIDs()
    if (ids.length === 0) return
    if (
      action === "delete" &&
      !window.confirm(
        t("pages.memory.confirm_bulk_delete", { count: ids.length }),
      )
    ) {
      return
    }
    void act(t("pages.memory.err_bulk"), async () => {
      const res = await bulkMemoryAction(activeStore as string, {
        action,
        type,
        ids,
      })
      clearSelection()
      if (res.failed && Object.keys(res.failed).length > 0) {
        // Reported rather than thrown: a bulk action over hundreds of rows
        // partially succeeds, and the operator needs to know which part.
        window.alert(
          t("pages.memory.bulk_partial", {
            changed: res.changed,
            failed: Object.keys(res.failed).length,
          }),
        )
      }
    })
  }

  const handleImport = (yaml: string, mode: "merge" | "replace") => {
    if (
      mode === "replace" &&
      !window.confirm(t("pages.memory.confirm_replace"))
    ) {
      return
    }
    void act(t("pages.memory.err_import"), async () => {
      const res = await importMemory(activeStore as string, yaml, mode)
      window.alert(
        t("pages.memory.import_done", {
          created: res.memories_created,
          skipped: res.memories_skipped,
          domains: res.domains_created,
        }),
      )
    })
  }

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
                  onClick={() => {
                    setSelected(s.id)
                    clearSelection()
                  }}
                  className={`flex w-full flex-col items-start rounded-md px-2 py-1.5 text-left text-sm ${
                    activeStore === s.id ? "bg-muted" : "hover:bg-muted/50"
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
          {activeStore === null ? (
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
                {detail.last_run ? (
                  <span>
                    {t("pages.memory.last_run")}:{" "}
                    {new Date(detail.last_run.started_at).toLocaleString()} —{" "}
                    {detail.last_run.trigger}/{detail.last_run.status} (
                    {detail.last_run.ops_applied})
                  </span>
                ) : (
                  <span>
                    {t("pages.memory.last_run")}: {t("pages.memory.never")}
                  </span>
                )}
              </div>

              {detail.last_run?.error && (
                <div className="text-destructive text-xs">
                  {detail.last_run.error}
                </div>
              )}

              {/* A note describes a run that SUCCEEDED — most often a contract
                  deviation the consolidator safely repaired. Muted, not
                  destructive: these used to share the error field and were
                  painted red, which read as a failure when nothing had failed. */}
              {detail.last_run?.note && (
                <div className="text-muted-foreground text-xs">
                  {detail.last_run.note}
                </div>
              )}

              <div className="flex flex-wrap items-center gap-2">
                <Button
                  size="sm"
                  variant={showRetired ? "secondary" : "outline"}
                  onClick={() => setShowRetired((v) => !v)}
                >
                  {showRetired
                    ? t("pages.memory.hide_retired")
                    : t("pages.memory.show_retired", {
                        count: detail.retired_count,
                      })}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setAddingDomain(true)}
                >
                  {t("pages.memory.add_domain")}
                </Button>
                <TransferControls
                  storeId={activeStore}
                  busy={busy}
                  onImport={handleImport}
                />
              </div>

              <BulkBar
                count={picked.size}
                busy={busy}
                onRetype={(ty) => bulk("retype", ty)}
                onRetire={() => bulk("retire")}
                onRestore={() => bulk("restore")}
                onDelete={() => bulk("delete")}
                onClear={clearSelection}
              />

              {addingDomain && (
                <AddDomainForm
                  busy={busy}
                  onCancel={() => setAddingDomain(false)}
                  onSubmit={(name, sticky) => {
                    setAddingDomain(false)
                    void act(t("pages.memory.err_add_domain"), () =>
                      createMemoryDomain(activeStore, { name, sticky }),
                    )
                  }}
                />
              )}

              {addingTo && (
                <AddMemoryForm
                  domain={addingTo}
                  busy={busy}
                  onCancel={() => setAddingTo(null)}
                  onSubmit={(type, text) => {
                    const domainID = addingTo.id
                    setAddingTo(null)
                    void act(t("pages.memory.err_add_memory"), () =>
                      createMemoryItem(activeStore, domainID, { type, text }),
                    )
                  }}
                />
              )}

              {detail.domains.map((d) => (
                <DomainCard
                  key={d.id}
                  d={d}
                  onDeleteDomain={handleDeleteDomain}
                  onAddMemory={setAddingTo}
                  rowActions={rowActions}
                />
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
